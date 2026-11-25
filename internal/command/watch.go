// What runs, said when it changes ( ADR 0024, 0035 ): the agent of the machine a command is typed on subscribes to the agent of
// every place it knows -- itself, and that of each machine a run started from here is on -- and passes on what they
// say. Nothing is asked again and again.
//
//	errand watch        a line each time a run starts, waits, ends or is forgotten, until Ctrl-C
//
// The same subscription is what a page or a screen will read.

package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

// placeSaid is what one place said: its table, or that it cannot be reached.
type placeSaid struct {
	key     string // "" for this machine
	machine string
	rows    []reach.TableRow
	lost    string
}

// knownHosts: the machines the notes of this point of entry name -- read from the notes as they are, asking nobody
func knownHosts() map[string]reach.HostSpec {
	out := map[string]reach.HostSpec{}
	files, _ := filepath.Glob(filepath.Join(runinfo.NotesDir(), "*.json"))
	for _, f := range files {
		var n runinfo.RunNote
		if data, err := os.ReadFile(f); err == nil && kit.JsonUnmarshal(data, &n) == nil && n.Host != nil {
			out[n.Host.Key()] = *n.Host
		}
	}
	return out
}

// subscribe: every place this point of entry knows, watched until `stop` -- each change sent to `said`
func subscribe(t *reach.Term, said chan<- placeSaid, stop <-chan struct{}) {
	watching := map[string]bool{}
	one := func(key string, h *reach.HostSpec) {
		name := "this machine"
		if h != nil {
			name = h.Host
		}
		tell := func(p placeSaid) bool {
			select {
			case said <- p:
				return true
			case <-stop:
				return false
			}
		}
		for {
			l, err := reach.LinkTo(t, h)
			if err == nil {
				c := l.Open(map[string]any{"op": "watch"})
				var got map[string]any
				if got, err = c.Reply(); err == nil {
					if host := kit.Text(got["host"]); host != "" && h != nil {
						name = host
					}
					for {
						kind, payload, rerr := c.Recv()
						if rerr != nil {
							err = rerr
							break
						}
						var table struct {
							Table []reach.TableRow `json:"table"`
						}
						if kind == kit.NOTE && json.Unmarshal(payload, &table) == nil && table.Table != nil {
							if !tell(placeSaid{key: key, machine: name, rows: table.Table}) {
								c.Close()
								return
							}
						}
					}
				}
				c.Abandon()
			}
			if !tell(placeSaid{key: key, machine: name, lost: err.Error()}) {
				return
			}
			select { // asked again later: a machine that comes back is watched again
			case <-stop:
				return
			case <-time.After(10 * time.Second):
			}
		}
	}
	for {
		if !watching[""] {
			watching[""] = true
			go one("", nil)
		}
		for key, h := range knownHosts() {
			if !watching[key] {
				watching[key] = true
				host := h
				go one(key, &host)
			}
		}
		select {
		case <-stop:
			return
		case <-time.After(time.Second): // ( a run started on a machine not watched yet: its note says so )
		}
	}
}

func watchMain(t *reach.Term, args []string) int {
	said, stop := make(chan placeSaid), make(chan struct{})
	defer close(stop)
	go subscribe(t, said, stop)
	seen := map[string]map[string]string{} // by place: each run's line, as last printed
	for {
		select {
		case <-t.Sigs:
			return 0
		case <-t.Gone:
			return 0
		case p := <-said:
			if p.lost != "" {
				t.Printf("  %s  cannot be reached: %s\n", p.machine, kit.FirstLine(p.lost))
				continue
			}
			was := seen[p.key]
			now := map[string]string{}
			for _, r := range p.rows {
				if r.Aside {
					continue
				}
				state := r.State
				if r.More != "" {
					state += ": " + r.More
				}
				now[r.ID] = state + "  " + r.What
				if was[r.ID] != now[r.ID] {
					t.Printf("  %s  %s  %s\n", p.machine, r.ID[:8], now[r.ID])
				}
			}
			for id := range was {
				if _, still := now[id]; !still {
					t.Printf("  %s  %s  forgotten\n", p.machine, id[:8])
				}
			}
			seen[p.key] = now
		}
	}
}
