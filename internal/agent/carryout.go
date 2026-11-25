package agent

import (
	"encoding/json"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// CarryOut: what does what is typed ( package command, which stands on this one )
var CarryOut func(t *reach.Term, verb string, args []string) int

// serveCommand: a command line, on a channel -- what it says goes to whoever typed it, what that one sends is the
// command's to read
func (a *Place) serveCommand(req map[string]any, f *reach.FarChan) {
	a.mu.Lock()
	a.busy++
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy--
		a.idleSince = time.Now()
		a.mu.Unlock()
	}()
	ttyOut, _ := req["tty_out"].(bool)
	ttyIn, _ := req["tty_in"].(bool)
	t := &reach.Term{Cwd: kit.Text(req["cwd"]), Environ: kit.Strs(req["environ"]), TtyOut: ttyOut, TtyIn: ttyIn, Emit: f.Out,
		Stdin: make(chan []byte), Sigs: make(chan syscall.Signal, 4), Detach: make(chan struct{}, 1), Gone: f.Gone, Answers: make(chan map[string]any, 1),
		Cols: int(kit.Num(req["cols"], 0)), Rows: int(kit.Num(req["rows"], 0))}
	f.Reply(map[string]any{"ok": true})
	done := make(chan struct{})
	go func() { // what whoever typed sends: what it was given to read, kept until it is read; its signals, at once
		var kept [][]byte
		over, feed := false, t.Stdin
		for {
			var head []byte
			to := feed
			if len(kept) > 0 {
				head = kept[0]
			} else if over && feed != nil {
				close(feed)
				feed, to = nil, nil
			} else {
				to = nil
			}
			select {
			case m := <-f.Inbox:
				switch m.Kind {
				case kit.IN:
					kept = append(kept, m.Payload)
				case kit.EOF:
					over = true
				case kit.RESIZE: // its window changed size
					var size map[string]any
					if json.Unmarshal(m.Payload, &size) == nil {
						t.Resized(int(kit.Num(size["cols"], 0)), int(kit.Num(size["rows"], 0)))
					}
				case kit.SIGNAL:
					var said map[string]any
					_ = json.Unmarshal(m.Payload, &said)
					if let, _ := said["detach"].(bool); let { // let go of the run: it is not stopped
						select {
						case t.Detach <- struct{}{}:
						default:
						}
						break
					}
					select {
					case t.Sigs <- syscall.SIGINT:
					default:
					}
				case kit.REPLY: // what it was asked ( the project's declarations )
					var got map[string]any
					if json.Unmarshal(m.Payload, &got) == nil {
						select {
						case t.Answers <- got:
						default:
						}
					}
				}
			case to <- head:
				kept = kept[1:]
			case <-f.Gone:
				return
			case <-done:
				return
			}
		}
	}()
	code := CarryOut(t, kit.Text(req["verb"]), kit.Strs(req["args"]))
	close(done)
	b, _ := json.Marshal(map[string]any{"code": code})
	f.Out(kit.EXIT, b)
}
