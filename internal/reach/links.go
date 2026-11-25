package reach

import (
	"fmt"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// ── the links of this command ────────────────────────────────────────────────

var (
	linksMu sync.Mutex
	links   = map[string]*Link{}
)

// LinkTo: the link to a place's agent -- a machine reached by ssh, kept between commands, or this one ( nil )
func LinkTo(t *Term, h *HostSpec) (*Link, error) {
	if h == nil { // this machine's: the command's own way to it, closed with the command
		if t.Here != nil {
			select {
			case <-t.Here.Dead:
			default:
				return t.Here, nil
			}
		}
		l, err := linkHere()
		if err == nil {
			t.Here = l
		}
		return l, err
	}
	if h.VM != nil {
		return t.MachineUp(h)
	}
	linksMu.Lock()
	defer linksMu.Unlock()
	if l := links[h.Key()]; l != nil {
		select {
		case <-l.Dead:
		default:
			return l, nil
		}
	}
	reached := *h // reached in the surroundings of whoever typed the command: its ssh, its keys
	reached.Environ, reached.Say = t.Environ, t.Say
	l, err := reached.Connect()
	if err != nil {
		return nil, err
	}
	links[h.Key()] = l
	return l, nil
}

// askInside: a request passed to another agent of this machine, which has one answer
func AskAgent(dir string, req map[string]any) (map[string]any, error) {
	l, err := LinkToAgent(dir)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	return l.Ask(req)
}

// ── reaching an agent of this machine ────────────────────────────────────────

func LinkToAgent(dir string) (*Link, error) {
	conn, err := kit.DialDir(dir, 3*time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := AwaitHello(conn); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})
	return NewLink(conn, conn, "this machine's agent"), nil
}

// linkHere: a link to this machine's agent, for a command it carries out itself: its own socket, as anybody's
func linkHere() (*Link, error) {
	conn, err := ReachAgent()
	if err != nil {
		return nil, err
	}
	if err := AwaitHello(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("this machine's agent: %w", err)
	}
	return NewLink(conn, conn, "this machine's agent"), nil
}
