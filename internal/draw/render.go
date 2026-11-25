// What draws a command's terminal ( ADR 0016 ): the side that drives. Off a terminal, a run's standard output is
// this command's and its standard error this command's, untouched, and errand's own lines go to standard error:
// `errand run cmd | grep …` is a pipeline. On a terminal, a run's output goes behind a gutter, between a header and
// the line that says how it ended, and what errand does while it works -- the step a run is at, what it waits for, a
// push under way -- is drawn where it belongs and erased when it is done, never part of the history: what errand does
// to get a run there goes on the right of the place line, and how long a run has run, with its step and what it waits
// for, is said under its last line, `┆ 41 s · iteration 41`, until the next thing it writes ( ADR 0016, "Revised" ).
// No line is kept at the bottom of the terminal.

package draw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// gutter: a run's output behind a gutter, as it comes. A run on a terminal writes what a terminal is written: a prompt
// with no end of line, a bar redrawn after a carriage return, colours. The gutter is put where a line begins -- after
// a carriage return too --, never held back until the line is whole, and the program's own colour is put back after
// it. A line the other stream left open is ended first, so that a stderr line never lands at the end of a progress
// bar.
type gutter struct {
	mu      sync.Mutex
	atStart map[byte]bool   // whether each stream is at the start of a line
	colour  map[byte][]byte // the colour the program has in force, put back after the gutter
	last    byte            // the stream that wrote last
	color   bool
	out     io.Writer
	lastAt  time.Time // when the run wrote last
}

const GutterWidth = 6 // what a gutter takes of a line ( a case is two columns in from its place, its bar two more ): the run's terminal is that much narrower

var Sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func newGutter(out io.Writer) *gutter {
	return &gutter{atStart: map[byte]bool{kit.OUT: true, kit.ERR: true}, colour: map[byte][]byte{}, out: out}
}

func (g *gutter) bar(kind byte) []byte {
	bar := "    │ "
	if kind == kit.ERR {
		bar = "    ┃ "
		if g.color {
			bar = "    \x1b[33m┃\x1b[0m "
		}
	} else if g.color {
		bar = "    \x1b[2m│\x1b[0m "
	}
	return []byte(bar)
}

func (g *gutter) write(kind byte, data []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastAt = time.Now()
	var out []byte
	if g.last != 0 && g.last != kind && !g.atStart[g.last] { // the other stream's line is left open: ended first
		out = append(out, '\n')
		g.atStart[g.last] = true
	}
	g.last = kind
	bar := g.bar(kind)
	for len(data) > 0 {
		var piece []byte
		var sep byte
		if i := bytes.IndexAny(data, "\r\n"); i < 0 {
			piece, data = data, nil
		} else {
			piece, sep, data = data[:i], data[i], data[i+1:]
			if sep == '\r' && len(data) > 0 && data[0] == '\n' { // a carriage return and a line feed end one line
				sep, data = '\n', data[1:]
			}
		}
		if len(piece) > 0 {
			if g.atStart[kind] {
				out = append(out, bar...)
				out = append(out, g.colour[kind]...)
				g.atStart[kind] = false
			}
			for _, m := range Sgr.FindAll(piece, -1) {
				if s := string(m); s == "\x1b[0m" || s == "\x1b[m" {
					g.colour[kind] = nil
				} else {
					g.colour[kind] = append(g.colour[kind], m...)
				}
			}
			out = append(out, piece...)
		}
		switch sep {
		case '\n':
			if g.atStart[kind] { // an empty line keeps the column of its block
				out = append(out, bytes.TrimRight(bar, " ")...)
			}
			out = append(out, '\n')
			g.atStart[kind] = true
		case '\r':
			out = append(out, '\r')
			g.atStart[kind] = true
		}
	}
	_, _ = g.out.Write(out)
}

// flush: a line left open is ended, so that what comes next -- the line that closes a run -- starts a line
func (g *gutter) flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for kind, atStart := range g.atStart {
		if !atStart {
			_, _ = g.out.Write([]byte("\n"))
			g.atStart[kind] = true
		}
	}
	g.colour = map[byte][]byte{}
}

// open: whether the run left a line unfinished -- a prompt
func (g *gutter) open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, atStart := range g.atStart {
		if !atStart {
			return true
		}
	}
	return false
}

// quietFor: whether the run has written nothing for `d`
func (g *gutter) quietFor(d time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.lastAt.IsZero() && time.Since(g.lastAt) >= d
}

type renderer struct {
	mu       sync.Mutex
	out, err io.Writer
	mode     DrawMode
	g        *gutter
	width    func() int // the terminal's width, when this command's output is one that can show what is transient
	color    bool
	typing   bool // what is typed goes to the run: it may be waiting for it

	// the place line: where a run goes, drawn before it, with what errand does to get there on its right
	place   *placeLine
	placed  bool // one was drawn for the run that comes: its header need not say where
	running bool // a run is followed: how long it has run is said while it is quiet
	since   time.Time
	// what is transient: under the last line of the run, until the next thing it writes
	work, wait, step string
	shown            string    // "line": a line under the last one; "hint": a hint after an open line; "": nothing
	drawn            string    // what is drawn of it, so that the same is not drawn again
	wrote            time.Time // when this command last wrote to the terminal
}

type placeLine struct {
	left, right string
	since       time.Time
	notes       []string
}

func NewRenderer(out, err io.Writer) *renderer {
	return &renderer{out: out, err: err, g: newGutter(out)}
}

// OnTerminal: this command's output is a terminal of that width, and shows what is transient
func (r *renderer) OnTerminal(width func() int, color bool) {
	r.width, r.color = width, color
	go func() { // a time that passes, a prompt nobody answers: seen without anything arriving
		for range time.Tick(500 * time.Millisecond) {
			r.mu.Lock()
			if r.width == nil {
				r.mu.Unlock()
				return
			}
			r.refresh()
			r.mu.Unlock()
		}
	}()
}

// the palette, by role ( ADR 0016 ): bold cyan for a case, bold magenta for where the cases run, green, red and yellow
// for how they ended, dim for everything errand does to get there
const (
	bold, dimmed, red, green, yellow, cyan = "\x1b[1m", "\x1b[2m", "\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[36m"
	placeColor                             = "\x1b[1m\x1b[35m"
	reset                                  = "\x1b[0m"
)

// paint ( under r.mu ): `text` in a colour, when the terminal shows colours
func (r *renderer) paint(code, text string) string {
	if r.mode.Color || (r.color && r.mode.Drawn) {
		return code + text + reset
	}
	return text
}

func (r *renderer) dim(text string) string { return r.paint(dimmed, text) }

// unshow ( under r.mu ): what was transient is taken away, before anything else is written
func (r *renderer) unshow() {
	r.drawn = ""
	switch r.shown {
	case "line":
		_, _ = r.out.Write([]byte("\r\x1b[K"))
	case "hint":
		_, _ = r.out.Write([]byte("\x1b[K"))
	}
	r.shown = ""
}

// before ( under r.mu ): a thing is about to be written to the terminal
func (r *renderer) before() {
	r.unshow()
	r.closePlace("")
	r.wrote = time.Now()
}

// refresh ( under r.mu ): what is transient, drawn if it should be
func (r *renderer) refresh() {
	if r.width == nil || !r.mode.Drawn {
		return
	}
	if r.place != nil {
		r.drawPlace()
		return
	}
	if !r.running && r.work == "" {
		r.unshow()
		return
	}
	cols := r.width()
	if r.g.open() { // a prompt: a hint after it, nothing under it
		hint := r.typing && r.g.quietFor(2*time.Second)
		switch {
		case hint && r.shown == "":
			_, _ = r.out.Write([]byte("\x1b7" + r.dim(" ⌨ waits for input?") + "\x1b8"))
			r.shown = "hint"
		case !hint && r.shown == "hint":
			r.unshow()
		}
		return
	}
	if r.shown == "hint" {
		r.unshow()
	}
	var bits []string
	if r.work != "" {
		bits = append(bits, r.work)
	}
	if r.wait != "" {
		bits = append(bits, r.wait)
	}
	if r.running && !r.since.IsZero() {
		took := int(time.Since(r.since).Seconds())
		bit := fmt.Sprintf("%d s", took)
		if took >= 60 {
			bit = fmt.Sprintf("%d min %02d s", took/60, took%60)
		}
		bits = append(bits, bit)
	}
	if r.step != "" {
		bits = append(bits, r.step)
	}
	quiet := time.Since(r.wrote) > time.Second || r.work != ""
	if len(bits) == 0 || !quiet {
		return
	}
	text := fit("    ┆ "+strings.Join(bits, " · "), max(10, cols-1))
	if r.shown == "line" && r.drawn == text {
		return
	}
	_, _ = r.out.Write([]byte("\r\x1b[K" + r.dim(text)))
	r.shown, r.drawn = "line", text
}

// drawPlace ( under r.mu ): the place line, with what is under way on its right
func (r *renderer) drawPlace() {
	if line := r.placeText(false); line != r.drawn {
		_, _ = r.out.Write([]byte("\r\x1b[K" + line))
		r.drawn = line
	}
}

func (r *renderer) placeText(final bool) string {
	p := r.place
	right := p.right
	if !final && r.work != "" {
		right = r.work + fmt.Sprintf(" · %d s", int(time.Since(p.since).Seconds()))
	}
	if right == "" {
		return r.paint(placeColor, p.left)
	}
	cols := 100
	if r.width != nil {
		cols = r.width()
	}
	gap := max(2, min(cols, 120)-1-len([]rune(p.left))-len([]rune(right)))
	return r.paint(placeColor, p.left) + strings.Repeat(" ", gap) + r.dim(right)
}

// closePlace ( under r.mu ): the place line is final, with its numbers, and what was said meanwhile under it
func (r *renderer) closePlace(numbers string) {
	if r.place == nil {
		return
	}
	r.place.right = numbers
	line := r.placeText(true)
	if r.width != nil && r.mode.Drawn {
		line = "\r\x1b[K" + line
	}
	_, _ = r.out.Write([]byte(line + "\n"))
	for _, note := range r.place.notes {
		_, _ = fmt.Fprintf(r.out, "  %s %s\n", r.dim("┆"), r.dim(note))
	}
	r.place, r.placed, r.drawn = nil, true, ""
	r.work = ""
}

// Close: nothing transient is left on the terminal
func (r *renderer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unshow()
	r.closePlace("")
	r.width = nil
}

// Frame: one thing the agent said of a command, drawn -> the command's exit code, once it said it
func (r *renderer) Frame(kind byte, payload []byte) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case kit.OUT, kit.ERR:
		r.before()
		if r.mode.Drawn {
			r.g.write(kind, payload)
		} else if kind == kit.OUT {
			_, _ = r.out.Write(payload)
		} else {
			_, _ = r.err.Write(payload)
		}
	case kit.NOTE:
		var e struct {
			Say    *string   `json:"say"`
			Print  *string   `json:"print"`
			Mode   *DrawMode `json:"mode"`
			Begin  *RunBegin `json:"begin"`
			End    *RunEnd   `json:"end"`
			Doing  *Doing    `json:"doing"`
			Place  *string   `json:"place"`
			Placed *string   `json:"placed"`
			Keys   *bool     `json:"keys"`
		}
		if json.Unmarshal(payload, &e) != nil {
			return 0, false
		}
		switch {
		case e.Mode != nil:
			r.unshow()
			r.g.flush()
			r.mode, r.g.color = *e.Mode, e.Mode.Color
		case e.Place != nil: // where a run goes, before it is there
			r.unshow()
			r.g.flush()
			r.place = &placeLine{left: "▸ " + *e.Place, since: time.Now()}
			r.wrote = time.Now()
			r.refresh()
		case e.Placed != nil:
			r.closePlace(*e.Placed)
			r.refresh()
		case e.Say != nil:
			if r.place != nil && r.mode.Drawn { // said on the way there: under the place line, in context
				r.place.notes = append(r.place.notes, strings.TrimSpace(*e.Say))
				break
			}
			r.before()
			if r.mode.Drawn {
				r.g.flush()
			}
			fmt.Fprintln(r.err, *e.Say)
		case e.Print != nil:
			r.before()
			fmt.Fprint(r.out, *e.Print)
		case e.Doing != nil:
			switch e.Doing.Part {
			case "work":
				r.work = e.Doing.Text
			case "wait":
				r.wait = e.Doing.Text
			case "step":
				if e.Doing.Step != nil {
					r.step = e.Doing.Step.line()
				} else {
					r.step = ""
				}
			}
			r.refresh()
		case e.Keys != nil:
			r.typing = *e.Keys
			r.refresh()
		case e.Begin != nil:
			r.before()
			if !r.placed { // where it runs, said here: a run here has no place line
				where := e.Begin.Host
				if b := e.Begin.Banner; b != "" && b != "here" {
					where += " → " + b
				}
				fmt.Fprintf(r.out, "%s\n", r.paint(placeColor, "▸ "+where))
			}
			fmt.Fprintf(r.out, "  %s\n", r.paint(bold+cyan, "▸ "+e.Begin.Label))
			for _, link := range e.Begin.Links {
				fmt.Fprintf(r.out, "    %s %s\n", r.paint(cyan, "↗"), r.paint("\x1b[2m", link))
			}
			r.placed, r.running, r.since = false, true, time.Now()
		case e.End != nil:
			r.before()
			r.running, r.wait, r.step = false, "", ""
			r.g.flush()
			end := r.err
			if r.mode.Drawn {
				end = r.out
			}
			// how it ended, behind the bar like what it wrote ( no corner: it does not always draw well )
			mark := e.End.Status
			if code := map[string]string{"PASS": green, "FAIL": red, "SKIP": yellow}[e.End.Status]; code != "" {
				mark = r.paint(bold+code, e.End.Status)
			}
			bar := string(r.g.bar(kit.OUT))
			fmt.Fprintf(end, "%s%s %s%s\n", bar, mark, e.End.Label, r.dim(e.End.Tail))
			if e.End.Output != "" {
				fmt.Fprintf(end, "%s%s\n", bar, r.dim("↳ "+e.End.Output))
			}
		}
	case kit.EXIT:
		r.before()
		r.running = false
		r.g.flush()
		var e map[string]any
		_ = json.Unmarshal(payload, &e)
		return int(kit.Num(e["code"], 1)), true
	}
	return 0, false
}

// what a run's beginning and end say, for whoever draws them
type RunBegin struct {
	Host   string `json:"host"`
	Banner string `json:"banner"`
	Label  string `json:"label"`
	// where it is to be seen: its page, its directory
	Links []string `json:"links,omitempty"`
}

type RunEnd struct {
	Status string `json:"status"`
	Label  string `json:"label"`
	Tail   string `json:"tail"`             // how long it took, its short results
	Output string `json:"output,omitempty"` // where its whole output is, when it failed
}

type DrawMode struct {
	Drawn bool `json:"drawn"` // a gutter, headers: for a person
	Color bool `json:"color"`
}

// ── what is transient ────────────────────────────────────────────────────────

// Doing: something errand does that is shown while it lasts -- said by the agent that does it, and said again empty
// when it is over
type Doing struct {
	Part string     `json:"part"` // work ( a push, a pull ), wait ( for the machine, an allocation, a queue ), step
	Text string     `json:"text,omitempty"`
	Step *StepState `json:"step,omitempty"` // where a run says it is ( ADR 0032 ), as the agent that holds it reads it
}

// StepState: where a run is, as its journal says
type StepState struct {
	Text   string             `json:"text,omitempty"`
	Done   *float64           `json:"done,omitempty"`
	Eta    *float64           `json:"eta,omitempty"` // seconds left, when two points say how fast it goes
	Curves map[string]float64 `json:"curves,omitempty"`
}

// line: `iteration 41 · ▕████      ▏ 41 % · 2 min left · residual 3e-07`
func (s *StepState) line() string {
	var parts []string
	if s.Text != "" {
		parts = append(parts, s.Text)
	}
	if s.Done != nil {
		d := math.Max(0, math.Min(1, *s.Done))
		full := int(math.Round(d * 10))
		parts = append(parts, fmt.Sprintf("▕%s%s▏ %.0f %%", strings.Repeat("█", full), strings.Repeat(" ", 10-full), d*100))
		if s.Eta != nil && d < 1 {
			parts = append(parts, Lasting(*s.Eta)+" left")
		}
	}
	names := make([]string, 0, len(s.Curves))
	for name := range s.Curves {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %.3g", name, s.Curves[name]))
	}
	return strings.Join(parts, " · ")
}

// Lasting: a time left, roughly
func Lasting(seconds float64) string {
	n := int(math.Round(seconds))
	switch {
	case n < 60:
		return fmt.Sprintf("%d s", n)
	case n < 3600:
		return fmt.Sprintf("%d min", (n+30)/60)
	}
	return fmt.Sprintf("%d h %02d min", n/3600, n%3600/60)
}

// fit: text cut to `cols` columns
func fit(text string, cols int) string {
	if r := []rune(text); len(r) > cols && cols > 1 {
		return string(r[:cols-1]) + "…"
	}
	return text
}

// RefreshNow: the window changed size
func (r *renderer) RefreshNow() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refresh()
}
