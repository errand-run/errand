// The far side of a link ( link.go ): what serves the requests a link carries -- an agent.

package reach

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// ── the far side ─────────────────────────────────────────────────────────────

// FarChan is one channel, from the far side: what an op is handed.
type FarChan struct {
	Out   func(kind byte, payload []byte)
	Req   map[string]any // what it was opened with, for whoever serves it
	Inbox chan LinkMsg   // what the near side sends on it, in order
	Gone  chan struct{}  // the near side closed it, or left
	Said  chan struct{}  // the near side closed it, on purpose: silence is not a stop ( ADR 0029 )
	once  sync.Once
}

func (f *FarChan) Reply(v any) {
	b, _ := json.Marshal(v)
	f.Out(kit.REPLY, b)
}

func (f *FarChan) Fail(format string, args ...any) {
	f.Reply(map[string]any{"error": fmt.Sprintf(format, args...)})
}

func (f *FarChan) leave() { f.once.Do(func() { close(f.Gone) }) }

// ServeLink: requests read from `r`, each served on its channel by `serve`, until the near side goes
func ServeLink(r io.Reader, w io.Writer, serve func(req map[string]any, f *FarChan)) {
	var wmu sync.Mutex
	out := bufio.NewWriterSize(w, 1<<16)
	write := func(b []byte) {
		wmu.Lock()
		_, _ = out.Write(b)
		_ = out.Flush()
		wmu.Unlock()
	}
	write([]byte(linkHello))
	chans := map[uint32]*FarChan{}
	var mu sync.Mutex
	var busy sync.WaitGroup
	for {
		ch, kind, payload, err := kit.ReadFrame(r)
		if err != nil {
			break
		}
		mu.Lock()
		f := chans[ch]
		mu.Unlock()
		switch {
		case kind == kit.OPEN:
			req := map[string]any{}
			dec := json.NewDecoder(bytes.NewReader(payload))
			dec.UseNumber()
			_ = dec.Decode(&req)
			f = &FarChan{Inbox: make(chan LinkMsg, 64), Gone: make(chan struct{}), Said: make(chan struct{})}
			id := ch
			f.Out = func(kind byte, payload []byte) { write(kit.Frame(id, kind, payload)) }
			mu.Lock()
			chans[ch] = f
			mu.Unlock()
			busy.Add(1)
			go func() {
				defer busy.Done()
				defer func() {
					if r := recover(); r != nil {
						f.Fail("%s", kit.SaidError(r))
					}
				}()
				serve(req, f)
			}()
		case f == nil:
		case kind == kit.CLOSE:
			close(f.Said)
			f.leave()
			mu.Lock()
			delete(chans, ch)
			mu.Unlock()
		default:
			select {
			case f.Inbox <- LinkMsg{kind, payload}:
			case <-f.Gone:
			}
		}
	}
	mu.Lock()
	for _, f := range chans {
		f.leave()
	}
	mu.Unlock()
	done := make(chan struct{})
	go func() { busy.Wait(); close(done) }()
	select { // what was being written is given the time to be left whole
	case <-done:
	case <-time.After(10 * time.Second):
	}
}
