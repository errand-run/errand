// A link between two errands: this program here, and this program on the machine a command reaches ( there.go ).
//
// One stream, both ways, carried by one `ssh` ( host.go ): frames `[channel][type][length][payload]`, as everywhere
// in errand. The near side opens channels ( `OPEN`, a JSON request that names its `op` ); the far side answers each on
// its channel ( `REPLY`, then whatever the op says, then nothing more ). Several channels live at once: a run is
// followed while its files are asked for.
//
// The far side says a line of its own before its first frame: what an interactive shell's profile printed on the way
// is skipped up to it.

package reach

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

const linkHello = "\x00errand-there/1\n"

type LinkMsg struct {
	Kind    byte
	Payload []byte
}

// Link is the near side.
type Link struct {
	w     io.WriteCloser
	wmu   sync.Mutex
	mu    sync.Mutex
	chans map[uint32]*Call
	next  uint32
	Dead  chan struct{}
	cmd   *exec.Cmd     // what carries it, when this side started it
	said  *bytes.Buffer // what that said beside the stream
	Where string
	base  *Link // what is asked goes through this one, as `wrap` puts it ( a machine errand boots )
	wrap  func(req any) any
}

// Call is one channel of a link, from the near side.
type Call struct {
	L     *Link
	ch    uint32
	inbox chan LinkMsg
}

var errLinkLost = errors.New("the connection was lost")

func NewLink(r io.Reader, w io.WriteCloser, where string) *Link {
	l := &Link{w: w, chans: map[uint32]*Call{}, Dead: make(chan struct{}), Where: where}
	go func() {
		defer close(l.Dead)
		for {
			ch, kind, payload, err := kit.ReadFrame(r)
			if err != nil {
				return
			}
			l.mu.Lock()
			c := l.chans[ch]
			l.mu.Unlock()
			if c == nil {
				continue
			}
			select {
			case c.inbox <- LinkMsg{kind, payload}:
			case <-time.After(5 * time.Minute): // nobody reads this channel any more: the others are not held for it
			}
		}
	}()
	return l
}

func (l *Link) write(b []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	_, err := l.w.Write(b)
	return err
}

func (l *Link) Open(req any) *Call {
	if l.base != nil {
		return l.base.Open(l.wrap(req))
	}
	l.mu.Lock()
	l.next++
	c := &Call{L: l, ch: l.next, inbox: make(chan LinkMsg, 64)}
	l.chans[c.ch] = c
	l.mu.Unlock()
	_ = l.write(kit.FrameJSON(c.ch, kit.OPEN, req))
	return c
}

func (l *Link) Close() {
	if l.base != nil {
		return
	}
	_ = l.w.Close()
	if l.cmd != nil {
		done := make(chan struct{})
		go func() { _ = l.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = l.cmd.Process.Kill()
		}
	}
}

func (c *Call) Send(kind byte, payload []byte) error {
	return c.L.write(kit.Frame(c.ch, kind, payload))
}

func (c *Call) SendJSON(kind byte, v any) error { return c.L.write(kit.FrameJSON(c.ch, kind, v)) }

func (c *Call) Recv() (byte, []byte, error) {
	select {
	case m := <-c.inbox:
		return m.Kind, m.Payload, nil
	default:
	}
	select {
	case m := <-c.inbox:
		return m.Kind, m.Payload, nil
	case <-c.L.Dead:
		select { // ( what arrived before it went is still read )
		case m := <-c.inbox:
			return m.Kind, m.Payload, nil
		default:
		}
		return 0, nil, errLinkLost
	}
}

// Reply: the answer to the request the channel was opened with; an `error` in it is an error
func (c *Call) Reply() (map[string]any, error) {
	for {
		kind, payload, err := c.Recv()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.L.Where, err)
		}
		if kind != kit.REPLY {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.UseNumber()
		got := map[string]any{}
		if err := dec.Decode(&got); err != nil {
			return nil, fmt.Errorf("%s: an answer that cannot be read: %v", c.L.Where, err)
		}
		if said, ok := got["error"]; ok {
			return nil, fmt.Errorf("%s: %v", c.L.Where, said)
		}
		return got, nil
	}
}

func (c *Call) Close() {
	_ = c.Send(kit.CLOSE, nil)
	c.L.mu.Lock()
	delete(c.L.chans, c.ch)
	c.L.mu.Unlock()
}

// Abandon: the channel left without a word -- what was asked on it goes on, and is no longer listened to
func (c *Call) Abandon() {
	c.L.mu.Lock()
	delete(c.L.chans, c.ch)
	c.L.mu.Unlock()
}

// Ask: a request that has one answer
func (l *Link) Ask(req any) (map[string]any, error) {
	c := l.Open(req)
	defer c.Close()
	return c.Reply()
}
