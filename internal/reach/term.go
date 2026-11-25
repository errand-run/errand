// A command's terminal, as the agent that carries the command out sees it ( ADR 0016, 0035 ): the cli reads what is
// typed and draws; the agent does the work, and what it says of it is events -- a line of errand's own, a run's beginning and end,
// a run's output as it comes -- which whoever drives draws as it likes ( draw/render.go ).
//
// A command never touches its process's own streams, directory or surroundings: an agent serves commands
// typed in other shells, on other days.

package reach

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
)

type Term struct {
	Cwd     string
	Environ []string
	TtyOut  bool                            // what the command prints is read by a person
	TtyIn   bool                            // what it reads is typed by one
	Emit    func(kind byte, payload []byte) // toward whoever draws
	Stdin   chan []byte                     // what it was given to read; closed at its end
	Sigs    chan syscall.Signal             // Ctrl-C and its kin, as they come
	Detach  chan struct{}                   // whoever typed the command asks to let go of the run it follows, which goes on
	Gone    chan struct{}                   // whoever typed the command went, without a word
	Answers chan map[string]any             // what whoever typed answers, when asked ( the project's declarations )
	Here    *Link                           // this machine's agent, as this command reaches it

	vmMu     sync.Mutex
	machines map[string]*Link // the machines errand boots this command uses ( vm.go )
	held     []*Call          // and what holds them up

	SizeMu   sync.Mutex
	Cols     int // the size of whoever typed the command's terminal, when it is one
	Rows     int
	watchers map[int]func(cols, rows int) // told when it changes
	nextSize int

	Tee io.Writer // -l: where what a run prints goes too

	Opened       int    // how many pages it opened in the browser
	PlaceOpen    bool   // a place line is drawn, and what is done to get there goes on its right
	PlaceNumbers string // what it took to get there, said on it when it is done
}

// Winsize: the size of the terminal the command was typed on, when it says one
func (t *Term) Winsize() (int, int) {
	t.SizeMu.Lock()
	defer t.SizeMu.Unlock()
	return t.Cols, t.Rows
}

// Resized: whoever typed the command changed the size of its window
func (t *Term) Resized(cols, rows int) {
	t.SizeMu.Lock()
	t.Cols, t.Rows = cols, rows
	var tell []func(int, int)
	for _, f := range t.watchers {
		tell = append(tell, f)
	}
	t.SizeMu.Unlock()
	for _, f := range tell {
		f(cols, rows)
	}
}

// OnResize: `f` is told whenever the window changes, until the returned function is called
func (t *Term) OnResize(f func(cols, rows int)) func() {
	t.SizeMu.Lock()
	defer t.SizeMu.Unlock()
	if t.watchers == nil {
		t.watchers = map[int]func(int, int){}
	}
	t.nextSize++
	id := t.nextSize
	t.watchers[id] = f
	return func() {
		t.SizeMu.Lock()
		delete(t.watchers, id)
		t.SizeMu.Unlock()
	}
}

// Doing: something errand does, shown while it lasts ( ADR 0016 ): on the place line's right when a run is on its way
// there, under the last line of a run otherwise. Said empty, it is over. Said only to a terminal: off one, nothing of it.
func (t *Term) Doing(part, text string) {
	if t.TtyOut {
		t.Event(map[string]any{"doing": draw.Doing{Part: part, Text: text}})
	}
}

// Working: `part` says `text` until the function returned is called
func (t *Term) Working(part, text string) func() {
	t.Doing(part, text)
	return func() { t.Doing(part, "") }
}

// Close: the command is through -- what it had opened for itself is closed
func (t *Term) Close() {
	t.vmMu.Lock()
	for _, c := range t.held {
		c.Close()
	}
	t.held, t.machines = nil, nil
	t.vmMu.Unlock()
	if t.Here != nil {
		t.Here.Close()
		t.Here = nil
	}
}

func (t *Term) Getenv(name string) string {
	out := ""
	for _, kv := range t.Environ {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			out = v
		}
	}
	return out
}

func (t *Term) Event(v any) {
	b, _ := json.Marshal(v)
	t.Emit(kit.NOTE, b)
}

// Say: a line of errand's own, about what it does
func (t *Term) Say(text string) { t.Event(map[string]any{"say": text}) }

func (t *Term) Sayf(format string, args ...any) {
	t.Say(strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}

func (t *Term) Sayln(args ...any) { t.Say(strings.TrimSuffix(fmt.Sprintln(args...), "\n")) }

// print: what a command answers -- a list, an id --, on its standard output
func (t *Term) print(text string) { t.Event(map[string]any{"print": text}) }

func (t *Term) Printf(format string, args ...any) { t.print(fmt.Sprintf(format, args...)) }

func (t *Term) Println(args ...any) { t.print(fmt.Sprintln(args...)) }

// Ask: a line asked of whoever typed the command, at its terminal -> it; false when they would not say
func (t *Term) Ask(text string) (string, bool) {
	t.Event(map[string]any{"ask": text})
	select {
	case got := <-t.Answers:
		if said, ok := got["answer"].(string); ok {
			return said, true
		}
		return "", false
	case <-t.Sigs:
		return "", false
	case <-t.Gone:
		return "", false
	}
}

// Output: what a run printed, as it printed it
func (t *Term) Output(kind byte, data []byte) {
	if t.Tee != nil {
		_, _ = t.Tee.Write(data)
	}
	t.Emit(kind, data)
}

// Red: what a person must see, on a terminal that shows colours
func (t *Term) Red(text string) string {
	if !t.TtyOut || t.Getenv("NO_COLOR") != "" {
		return text
	}
	return "\x1b[31m" + text + "\x1b[0m"
}
