// `errand tui`: what the page shows, in the terminal ( page/model.go builds both ). The list on the left -- runs, machines,
// storage --, the detail of the row under the cursor on the right.

package tui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/page"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

const tuiHint = "1 runs · 2 machines · 3 caches · ↑↓ choose · →← unfold, fold · tab detail · enter open · e edit · k kill · f forget · r rename · a watch · w page · / filter · ? keys · q quit"

var tuiKeys = [][2]string{
	{"1 2 3", "the runs, the machines, the caches and storage"},
	{"↑ ↓ pgup pgdn home end", "the cursor; the wheel scrolls"},
	{"→ ←", "unfold, fold"},
	{"tab", "the list, or the detail"},
	{"enter", "unfold, or open the output under the cursor"},
	{"/ esc", "filter; what was filtered, shown again"},
	{"p", "this project only, or every one"},
	{"o", "the whole output, in the pager"},
	{"e", "the file of the run, in the editor"},
	{"x", "a shell where the run is"},
	{"w", "its page, in the browser"},
	{"k", "stop what is under the cursor, once confirmed"},
	{"shift ↑ ↓", "rows chosen together, for d and D"},
	{"d D", "runs removed; D keeps the latest of each"},
	{"s", "a table of parameters, sorted by another column"},
	{"c b", "the runs other machines hold; one of them brought here"},
	{"f r a", "a machine forgotten, renamed, watched or not"},
	{"i u h n v", "caches: the dead removed, the unused, held here, after how many days, every entry"},
	{"R", "everything asked again"},
	{"q", "quit"},
}

type tuiPane struct {
	top, cursor int
	n           int
	height      int
}

func (p *tuiPane) move(d int) {
	p.cursor = max(0, min(p.n-1, p.cursor+d))
	p.reveal()
}

func (p *tuiPane) reveal() {
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.height > 0 && p.cursor >= p.top+p.height {
		p.top = p.cursor - p.height + 1
	}
	p.top = max(0, min(p.top, max(0, p.n-p.height)))
}

func (p *tuiPane) scroll(d int) { p.top = max(0, min(p.top+d, max(0, p.n-p.height))) }

type tui struct {
	u        *page.Ui
	out      *bufio.Writer
	cols     int
	rows     int
	tab      string
	query    string
	typing   bool
	only     bool
	mine     string
	folded   map[string]bool
	opened   map[string]bool
	days     float64
	every    bool
	list     []page.UiRow
	sel      string
	touched  bool // the cursor was moved by hand
	marked   map[string]bool
	anchor   int
	detail   []page.UiDetail
	detailOf string
	detailM  *page.UiMachine
	read     page.UiRead
	left     tuiPane
	right    tuiPane
	active   int
	message  string
	help     bool
	box      *tuiBox
	mu       sync.Mutex
	poke     chan struct{}
	restore  func()
	keys     chan string
	reading  sync.Mutex // who reads what is typed
}

// tuiBox is a question, in the middle: yes or no, or a line of text.
type tuiBox struct {
	title, verb string
	lines       []string
	danger      bool
	prompt      bool
	text        string
	do          func(text string)
}

// TuiMain: `errand tui`
func TuiMain() int {
	if !draw.IsTerminal(os.Stdout) || !draw.IsTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "errand tui needs a terminal")
		return 2
	}
	u, err := page.NewUI()
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand tui:", err)
		return 1
	}
	s := &tui{u: u, out: bufio.NewWriter(os.Stdout), tab: "runs", folded: map[string]bool{}, opened: map[string]bool{}, days: 30, marked: map[string]bool{},
		poke: make(chan struct{}, 1), keys: make(chan string, 64)}
	cwd, _ := os.Getwd()
	for d := cwd; ; d = filepath.Dir(d) {
		if found, _ := filepath.Glob(filepath.Join(d, "errand-*.py")); len(found) > 0 {
			s.mine = d
			break
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	wake := func() {
		select {
		case s.poke <- struct{}{}:
		default:
		}
	}
	u.Changed = func(bool, []string) { wake() }
	u.Said = func(text string) { s.mu.Lock(); s.message = text; s.mu.Unlock(); wake() }
	code := s.loop()
	u.Close()
	return code
}

func (s *tui) enter() {
	s.restore, _ = draw.RawKeys(os.Stdin, true)
	draw.ReadsReturn(os.Stdin) // a read that comes back, typed or not: the terminal is lent at times
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
}

func (s *tui) leave() {
	fmt.Fprint(os.Stdout, "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[?1049l")
	if s.restore != nil {
		s.restore()
	}
}

// readKeys: what is typed, a key at a time -- while nobody else has the terminal
func (s *tui) readKeys() {
	buf := make([]byte, 4096)
	for {
		s.reading.Lock()
		n, _ := syscall.Read(0, buf)
		s.reading.Unlock()
		if n <= 0 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		data := string(buf[:n])
		for data != "" {
			k := data[:1]
			switch {
			case strings.HasPrefix(data, "\x1b["), strings.HasPrefix(data, "\x1bO"):
				end := 2
				for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
					end++
				}
				k = data[:min(len(data), end+1)]
			default:
				_, size := utf8.DecodeRuneInString(data)
				k = data[:size]
			}
			data = data[len(k):]
			s.keys <- k
		}
	}
}

func (s *tui) loop() int {
	s.enter()
	defer s.leave()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, system.Resized, syscall.SIGTERM, syscall.SIGHUP)
	go s.readKeys()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	s.build()
	s.draw()
	for {
		select {
		case sig := <-sigs:
			if sig != system.Resized {
				return 0
			}
		case k, ok := <-s.keys:
			if !ok || s.key(k) {
				return 0
			}
			for more := true; more; { // what was typed meanwhile is dealt with before the next picture
				select {
				case k, ok := <-s.keys:
					if !ok || s.key(k) {
						return 0
					}
				default:
					more = false
				}
			}
		case <-s.poke:
		case <-tick.C:
		}
		s.build()
		s.draw()
	}
}

func (s *tui) row() *page.UiRow {
	if s.left.cursor < len(s.list) {
		return &s.list[s.left.cursor]
	}
	return nil
}

func (s *tui) build() {
	q := page.UiAsked{Tab: s.tab, Query: s.query, Folded: s.folded, Opened: s.opened, Days: s.days, All: s.every}
	if s.only {
		q.Project = s.mine
	}
	if s.tab == "runs" {
		q.Sel = s.sel
	}
	s.list = s.u.Build(q)
	s.left.n = len(s.list)
	at := -1
	for i := range s.list {
		if s.list[i].Key == s.sel {
			at = i
		}
	}
	if at < 0 && s.sel == "" { // at first, on what runs, else on what was run
		for i := range s.list {
			if k := s.list[i].Kind; k == "proc" || k == "run" || k == "machine" {
				at, s.sel = i, s.list[i].Key
				break
			}
		}
	}
	if at >= 0 {
		s.left.cursor = at
	} else {
		s.left.cursor = max(0, min(s.left.cursor, len(s.list)-1))
		if r := s.row(); r != nil && s.touched {
			s.sel = r.Key
		}
	}
	r := s.row()
	if r == nil {
		s.detail = nil
		return
	}
	s.detail, s.detailM, s.read = s.u.DetailOf(r)
	if s.detailOf != r.Key {
		s.detailOf, s.right = r.Key, tuiPane{height: s.right.height}
	}
	s.right.n = len(s.detail)
	s.right.cursor = max(0, min(s.right.cursor, s.right.n-1))
}

// ── drawing ──────────────────────────────────────────────────────────────────

const (
	tuiReset = "\x1b[0m"
	tuiDim   = "\x1b[2m"
	tuiBold  = "\x1b[1m"
	tuiCyan  = "\x1b[36m"
	tuiGreen = "\x1b[32m"
	tuiRed   = "\x1b[31m"
	tuiWarn  = "\x1b[33m"
	tuiTint  = "\x1b[48;5;237m"
)

var tuiColors = os.Getenv("NO_COLOR") == ""

var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func paint(code, text string) string {
	if !tuiColors && code != tuiBold && code != tuiTint && code != tuiDim {
		return text
	}
	return code + text + tuiReset
}

func fitTo(text string, width int) string {
	r := []rune(text)
	if len(r) <= width {
		return text + strings.Repeat(" ", width-len(r))
	}
	if width < 1 {
		return ""
	}
	return string(r[:width-1]) + "…"
}

func markOf(mark string) (string, string) {
	switch mark {
	case "run":
		return "●", tuiCyan
	case "wait":
		return "◌", tuiWarn
	case "ok":
		return "✓", tuiGreen
	case "FAIL":
		return "✗", tuiRed
	case "skip":
		return "∅", tuiDim
	case "lost":
		return "?", tuiWarn
	case "going":
		return "…", tuiDim
	case "there":
		return "⇣", tuiDim
	}
	return " ", ""
}

func gauge(f float64) string {
	i := max(0, min(8, int(f*8+0.5)))
	return []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}[i]
}

func (s *tui) drawRow(i, width int) string {
	r := s.list[i]
	fold := "  "
	if r.Open != nil {
		fold = "▸ "
		if *r.Open {
			fold = "▾ "
		}
	}
	mark, color := markOf(r.Mark)
	head := strings.Repeat("  ", r.Depth) + fold
	label := r.Label
	detail := r.Detail
	for _, m := range r.Meters {
		f, _ := m[1].(float64)
		detail = fmt.Sprintf("%v %s  ", m[0], gauge(f)) + detail
	}
	room := width - len([]rune(head)) - 3
	if d := len([]rune(detail)); d > 0 {
		room -= d + 1
	}
	text := fitTo(label, max(8, room))
	line := head
	if r.Mark != "" {
		line += paint(color, mark) + " "
	} else {
		line += "  "
	}
	switch {
	case r.Kind == "day" || r.Kind == "machine" || r.Kind == "mount":
		line += paint(tuiBold, text)
	case r.Kind == "thead" || r.Kind == "note" || r.Kind == "dormant":
		line += paint(tuiDim, text)
	case r.Kind == "broken":
		line += paint(tuiRed, text)
	case r.Level == "dir":
		line += paint(tuiCyan, text)
	default:
		line += text
	}
	if detail != "" {
		line += " " + paint(tuiDim, detail)
	}
	plain := len([]rune(escapes.ReplaceAllString(line, "")))
	if plain < width {
		line += strings.Repeat(" ", width-plain)
	}
	if i == s.left.cursor || s.marked[r.Key] {
		code := tuiTint
		if i == s.left.cursor && s.active == 0 {
			code = "\x1b[7m"
		}
		line = code + strings.ReplaceAll(line, tuiReset, tuiReset+code) + tuiReset
	}
	return line
}

func (s *tui) drawDetail(i, width int) string {
	d := s.detail[i]
	label := escapes.ReplaceAllString(d.Label, "")
	line := ""
	switch d.Kind {
	case "plain":
		line = paint(tuiDim, fitTo(d.K, 10)) + " " + fitTo(d.V, max(4, width-11))
	case "head":
		line = paint(tuiBold, fitTo(label, width))
	case "output":
		detail := d.Detail
		line = "  " + fitTo(label, max(8, width-4-len([]rune(detail)))) + " " + paint(tuiDim, detail)
	case "chead":
		line = paint(tuiCyan, fitTo(label, width))
	case "cend":
		color := tuiGreen
		if d.Status == "FAIL" {
			color = tuiRed
		}
		line = paint(color, fitTo(label, width))
	case "broken":
		line = paint(tuiRed, fitTo(label, width))
	case "out":
		line = fitTo(label, width)
	default:
		line = paint(tuiDim, fitTo(label, width))
	}
	plain := len([]rune(escapes.ReplaceAllString(line, "")))
	if plain < width {
		line += strings.Repeat(" ", width-plain)
	}
	if i == s.right.cursor && s.active == 1 {
		line = "\x1b[7m" + strings.ReplaceAll(line, tuiReset, tuiReset+"\x1b[7m") + tuiReset
	}
	return line
}

func (s *tui) draw() {
	cols, rows, ok := draw.TermSize(os.Stdout)
	if !ok {
		cols, rows = 80, 24
	}
	s.cols, s.rows = cols, rows
	body := rows - 2
	lw := cols * 55 / 100
	if cols < 90 {
		lw = cols
	}
	rw := cols - lw - 1
	s.left.height, s.right.height = body, body
	s.left.reveal()
	s.right.top = max(0, min(s.right.top, max(0, s.right.n-body)))
	var b strings.Builder
	b.WriteString("\x1b[H")
	// the tabs
	head := " "
	for i, name := range []string{"runs", "machines", "caches"} {
		label := fmt.Sprintf(" %d %s ", i+1, name)
		if name == s.tab {
			head += "\x1b[7m" + label + tuiReset + " "
		} else {
			head += paint(tuiDim, label) + " "
		}
	}
	if s.query != "" || s.typing {
		cursor := ""
		if s.typing {
			cursor = "▏"
		}
		head += paint(tuiWarn, " / "+s.query+cursor)
	}
	if s.only {
		head += paint(tuiWarn, "  "+path.Base(s.mine)+" only")
	}
	b.WriteString(head + "\x1b[K\r\n")
	for y := 0; y < body; y++ {
		if i := s.left.top + y; i < len(s.list) {
			b.WriteString(s.drawRow(i, lw))
		} else {
			b.WriteString(strings.Repeat(" ", lw))
		}
		if rw > 10 {
			b.WriteString(paint(tuiDim, "│"))
			if i := s.right.top + y; i < len(s.detail) {
				b.WriteString(s.drawDetail(i, rw))
			}
		}
		b.WriteString("\x1b[K\r\n")
	}
	s.mu.Lock()
	foot := s.message
	s.mu.Unlock()
	if foot == "" {
		foot = tuiHint
	}
	b.WriteString(paint(tuiDim, fitTo(foot, cols-1)) + "\x1b[K")
	switch {
	case s.help:
		var lines []string
		for _, k := range tuiKeys {
			lines = append(lines, fitTo(k[0], 24)+" "+k[1])
		}
		s.window(&b, "keys", lines, "any key: back", false)
	case s.box != nil:
		lines := s.box.lines
		foot := "enter: " + s.box.verb + "   ·   esc: nothing"
		if s.box.prompt {
			lines = append(append([]string{}, lines...), "", s.box.text+"▏")
		}
		s.window(&b, s.box.title, lines, foot, s.box.danger)
	}
	_, _ = s.out.WriteString(b.String())
	_ = s.out.Flush()
}

func (s *tui) window(b *strings.Builder, title string, lines []string, foot string, danger bool) {
	w := len([]rune(title)) + 4
	for _, l := range append(append([]string{}, lines...), foot) {
		w = max(w, len([]rune(l))+4)
	}
	w = min(w, s.cols-2)
	h := min(len(lines)+4, s.rows-2)
	x, y := (s.cols-w)/2, (s.rows-h)/2
	color := tuiCyan
	if danger {
		color = tuiRed
	}
	put := func(row int, text string) { fmt.Fprintf(b, "\x1b[%d;%dH%s", y+row+1, x+1, text) }
	put(0, paint(color, "┌"+strings.Repeat("─", w-2)+"┐"))
	put(1, paint(color, "│")+" "+paint(tuiBold, fitTo(title, w-4))+" "+paint(color, "│"))
	for i := 0; i < h-4; i++ {
		put(2+i, paint(color, "│")+" "+fitTo(lines[i], w-4)+" "+paint(color, "│"))
	}
	put(h-2, paint(color, "│")+" "+paint(tuiDim, fitTo(foot, w-4))+" "+paint(color, "│"))
	put(h-1, paint(color, "└"+strings.Repeat("─", w-2)+"┘"))
}

// ── keys ─────────────────────────────────────────────────────────────────────

func (s *tui) say(text string) { s.mu.Lock(); s.message = text; s.mu.Unlock() }

func (s *tui) pane() *tuiPane {
	if s.active == 1 {
		return &s.right
	}
	return &s.left
}

func (s *tui) moved() {
	s.touched = true
	if r := s.row(); r != nil {
		s.sel = r.Key
	}
}

func (s *tui) key(k string) (quit bool) {
	switch {
	case s.help:
		s.help = false
		return false
	case s.box != nil:
		box := s.box
		switch k {
		case "\x1b":
			s.box = nil
			s.say("nothing done")
		case "\r", "\n":
			s.box = nil
			box.do(box.text)
		case "\x7f", "\b":
			if r := []rune(box.text); box.prompt && len(r) > 0 {
				box.text = string(r[:len(r)-1])
			}
		default:
			if box.prompt && !strings.HasPrefix(k, "\x1b") && k >= " " {
				box.text += k
			}
		}
		return false
	case s.typing:
		switch k {
		case "\x1b":
			s.typing, s.query = false, ""
		case "\r", "\n":
			s.typing = false
		case "\x7f", "\b":
			if r := []rune(s.query); len(r) > 0 {
				s.query = string(r[:len(r)-1])
			}
		default:
			if !strings.HasPrefix(k, "\x1b") && k >= " " {
				s.query += k
			}
		}
		return false
	}
	s.say("")
	if strings.HasPrefix(k, "\x1b[<") {
		s.mouse(k)
		return false
	}
	p := s.pane()
	unmark := func() { s.marked = map[string]bool{} }
	switch k {
	case "q", "Q", "\x03":
		return true
	case "\x1b[B", "\x1bOB":
		unmark()
		p.move(1)
		s.moved()
	case "\x1b[A", "\x1bOA":
		unmark()
		p.move(-1)
		s.moved()
	case "\x1b[1;2B", "\x1b[1;2A":
		if s.tab == "runs" && s.active == 0 {
			if len(s.marked) == 0 {
				s.anchor = s.left.cursor
			}
			d := 1
			if k == "\x1b[1;2A" {
				d = -1
			}
			s.left.move(d)
			s.moved()
			s.marked = map[string]bool{}
			for i := min(s.anchor, s.left.cursor); i <= max(s.anchor, s.left.cursor) && i < len(s.list); i++ {
				s.marked[s.list[i].Key] = true
			}
		}
	case "\x1b[6~":
		p.move(max(1, p.height-1))
		s.moved()
	case "\x1b[5~":
		p.move(-max(1, p.height-1))
		s.moved()
	case "\x1b[H", "\x1bOH", "\x1b[1~":
		p.move(-1 << 30)
		s.moved()
	case "\x1b[F", "\x1bOF", "\x1b[4~":
		p.move(1 << 30)
		s.moved()
	case "\t", "\x1b[Z":
		s.active = 1 - s.active
		if s.active == 1 { // on the first output
			for i, d := range s.detail {
				if d.Kind == "output" {
					s.right.cursor = i
					s.right.reveal()
					break
				}
			}
		}
	case "\x1b[C", "\x1bOC":
		s.fold(true)
	case "\x1b[D", "\x1bOD":
		s.fold(false)
	case "1", "2", "3":
		s.tab = []string{"runs", "machines", "caches"}[k[0]-'1']
		s.left, s.sel, s.active = tuiPane{height: s.left.height}, "", 0
		unmark()
	case "\r", "\n":
		s.open()
	case "/":
		s.typing = true
	case "\x1b":
		if len(s.marked) > 0 {
			unmark()
		} else {
			s.query = ""
		}
	case "p":
		if s.mine == "" {
			s.say("not in a project: every project is shown")
		} else {
			s.only = !s.only
		}
	case "?":
		s.help = true
	case "k":
		s.kill()
	case "f":
		s.forget()
	case "a":
		if r := s.row(); r != nil && s.detailM != nil && !s.detailM.Here() && (r.Kind == "machine" || r.Kind == "dormant" || r.Kind == "broken") {
			s.say(s.u.ToggleWatch(s.detailM))
		}
	case "r":
		s.rename()
	case "R":
		s.u.Mu.Lock()
		s.u.ReadAt, s.u.PastAt = time.Time{}, time.Time{}
		s.u.Mu.Unlock()
		page.ReadsMu.Lock()
		page.Reads = map[string]page.UiRead{}
		page.ReadsMu.Unlock()
		s.say("asking again")
	case "w":
		s.browse()
	case "o":
		s.whole()
	case "e":
		s.edit()
	case "x":
		s.shell()
	case "s":
		s.sortColumn()
	case "c":
		if s.tab == "runs" {
			s.say("checking the other machines…")
			go s.u.Check()
		}
	case "b":
		if r := s.row(); r != nil {
			if past, ok := r.What.(*runinfo.PastRun); ok && past.There != "" {
				s.say("bringing " + path.Base(past.Rel) + " back…")
				go s.u.BringBack(past)
			} else {
				s.say("b: on a run the check found on another machine only")
			}
		}
	case "d", "D", "i", "u", "h", "n", "v":
		s.removing(k)
	}
	return false
}

func (s *tui) mouse(k string) {
	var b, x, y int
	var end rune
	if n, _ := fmt.Sscanf(k, "\x1b[<%d;%d;%d%c", &b, &x, &y, &end); n != 4 {
		return
	}
	lw := s.cols * 55 / 100
	if s.cols < 90 {
		lw = s.cols
	}
	p, which := &s.left, 0
	if x > lw+1 {
		p, which = &s.right, 1
	}
	switch {
	case b == 64:
		p.scroll(-3)
	case b == 65:
		p.scroll(3)
	case b == 0 && end == 'M' && y >= 2 && y < s.rows:
		s.active = which
		if i := p.top + y - 2; i < p.n {
			p.cursor = i
			if which == 0 {
				s.moved()
				if r := s.row(); r != nil && r.Open != nil && x-1-2*r.Depth < 2 {
					s.fold(!*r.Open)
				}
			}
		}
	}
}

func (s *tui) fold(open bool) {
	r := s.row()
	if r == nil || s.active == 1 {
		return
	}
	if r.Open == nil || r.Fold == nil || *r.Open == open {
		if !open { // what it is in, folded
			for i := s.left.cursor - 1; i >= 0; i-- {
				if s.list[i].Open != nil && s.list[i].Depth < r.Depth {
					s.left.cursor = i
					s.moved()
					break
				}
			}
		}
		return
	}
	f := *r.Fold
	tree := strings.HasPrefix(f, "t:") || strings.HasPrefix(f, "entries:")
	if open {
		delete(s.folded, f)
		if tree {
			s.opened[f] = true
		}
	} else {
		delete(s.opened, f)
		s.folded[f] = true
	}
}

func (s *tui) open() {
	if s.active == 1 {
		if s.right.cursor < len(s.detail) {
			if d := s.detail[s.right.cursor]; d.Kind == "output" {
				switch {
				case d.URL != "":
					s.outside(opener(), d.URL)
				case d.Path == "":
					s.whole()
				default:
					s.show(d.Path)
				}
			}
		}
		return
	}
	if r := s.row(); r != nil && r.Open != nil {
		s.fold(!*r.Open)
	} else {
		s.active = 1
	}
}

func opener() string {
	if _, err := exec.LookPath("open"); err == nil {
		return "open"
	}
	return "xdg-open"
}

// outside: a program the terminal is given to until it ends
func (s *tui) outside(argv ...string) {
	s.reading.Lock()
	defer s.reading.Unlock()
	s.leave()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	s.enter()
	if err != nil {
		s.say(argv[0] + ": " + err.Error())
	}
}

// local: a file of a machine, on this one
func (s *tui) local(p string) (string, error) {
	if s.detailM == nil || s.detailM.Here() {
		return p, nil
	}
	dir, err := os.MkdirTemp("", "errand-tui-")
	if err != nil {
		return "", err
	}
	to := filepath.Join(dir, path.Base(p))
	f, err := os.Create(to)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for offset := 0.0; offset < 64<<20; {
		got, err := s.u.Ask(s.detailM, map[string]any{"op": "tail", "path": p, "offset": offset, "size": 1 << 20})
		if err != nil {
			return "", err
		}
		_, _ = f.WriteString(kit.Text(got["data"]))
		end := kit.Num(got["end"], 0)
		if end == kit.Num(got["offset"], 0) || end >= kit.Num(got["size"], 0) {
			break
		}
		offset = end
	}
	return to, nil
}

func (s *tui) show(p string) {
	local, err := s.local(p)
	if err != nil {
		s.say(kit.FirstLine(err.Error()))
		return
	}
	if page.Rich(p) {
		_ = exec.Command(opener(), local).Start()
		return
	}
	pager := strings.Fields(os.Getenv("PAGER"))
	if len(pager) == 0 {
		pager = []string{"less", "-R"}
	}
	s.outside(append(pager, local)...)
}

func (s *tui) whole() {
	if s.read.Path == "" {
		s.say("no output to show")
		return
	}
	s.show(s.read.Path)
}

func (s *tui) edit() {
	r := s.row()
	if r == nil {
		return
	}
	p := page.EntryFile(r)
	if s.active == 1 && s.right.cursor < len(s.detail) && s.detail[s.right.cursor].Path != "" {
		p = s.detail[s.right.cursor].Path
	}
	if p == "" || s.detailM == nil || !s.detailM.Here() {
		s.say("e: on a run of this machine, or one of its files")
		return
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	s.outside(append(strings.Fields(editor), p)...)
}

func (s *tui) shell() {
	r := s.row()
	if r == nil || s.detailM == nil {
		return
	}
	dir := page.RowLeaf(r)
	if t, ok := r.What.(*reach.TableRow); ok {
		dir = t.Project
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	if h := s.detailM.M.Spec(); h != nil {
		ssh := os.Getenv("ERRAND_SSH")
		if ssh == "" {
			ssh = "ssh"
		}
		line := "exec $SHELL -l"
		if dir != "" {
			line = "cd " + reach.ShellQuote(dir) + " && " + line
		}
		s.outside(append(append([]string{ssh, "-t"}, h.Options...), h.Host, line)...)
		return
	}
	line := "exec " + shell
	if dir != "" {
		line = "cd " + reach.ShellQuote(dir) + " && " + line
	}
	s.outside("sh", "-c", line)
}

func (s *tui) browse() {
	out, err := exec.Command(kit.SelfPath(), "web", "once").Output()
	if err != nil {
		s.say("the page does not come")
		return
	}
	link := strings.TrimSpace(string(out))
	if r := s.row(); r != nil && page.RowLeaf(r) != "" {
		base, code, _ := strings.Cut(link, "?")
		link = base + "r/" + runinfo.RunIdent(page.RowLeaf(r)) + "?" + code
	}
	if os.Getenv("ERRAND_NO_BROWSER") != "" || exec.Command(opener(), link).Run() != nil {
		s.say(link)
	}
}

func (s *tui) kill() {
	r := s.row()
	if r == nil {
		return
	}
	list := s.u.KillList(r)
	if len(list) == 0 {
		s.say("put the cursor on something that runs")
		return
	}
	var lines []string
	for _, k := range list {
		lines = append(lines, k.What+" on "+k.M.Name())
	}
	s.box = &tuiBox{title: fmt.Sprintf("stop %d?", len(list)), verb: "stop", lines: lines, danger: true, do: func(string) { s.say(s.u.Kill(list)) }}
}

func (s *tui) forget() {
	m := s.detailM
	if r := s.row(); r == nil || m == nil || m.Here() || (r.Kind != "machine" && r.Kind != "dormant" && r.Kind != "broken") {
		s.say("f: on another machine")
		return
	}
	s.box = &tuiBox{title: "forget " + m.Name() + "?", verb: "forget", lines: []string{"not asked any more, until work is sent there again", "what runs there goes on"}, do: func(string) { s.say(s.u.Forget(m)) }}
}

func (s *tui) rename() {
	m := s.detailM
	if r := s.row(); r == nil || m == nil || m.Here() || (r.Kind != "machine" && r.Kind != "dormant" && r.Kind != "broken") {
		s.say("r: on another machine")
		return
	}
	s.box = &tuiBox{title: "a name for " + m.Name(), verb: "keep it", prompt: true, text: m.Name(), do: func(t string) {
		t = strings.TrimSpace(t)
		if err := m.M.CanBeNamed(t); err != nil {
			s.say(err.Error())
			return
		}
		s.u.EditNotes(m, func(n *reach.MachineNote) { n.Label = t })
		s.say("named " + t)
	}}
}

func (s *tui) sortColumn() {
	for i := s.left.cursor; i >= 0 && i < len(s.list); i-- {
		h := s.list[i]
		if h.Kind != "thead" {
			continue
		}
		var names []string
		for _, c := range h.Cols {
			names = append(names, fmt.Sprint(c[0]))
		}
		now := ""
		for k := range s.opened {
			if spec, ok := strings.CutPrefix(k, "s:"+h.Tree+"="); ok {
				now = spec
				delete(s.opened, k)
			}
		}
		// each column up, then down, then as it was
		next := ""
		switch {
		case now == "":
			next = names[0]
		case !strings.HasPrefix(now, "-"):
			next = "-" + now
		default:
			for j, n := range names {
				if n == now[1:] && j+1 < len(names) {
					next = names[j+1]
				}
			}
		}
		if next != "" {
			s.opened["s:"+h.Tree+"="+next] = true
		}
		s.say("sorted by " + next)
		return
	}
	s.say("s: on a table of parameters, to sort it")
}

func (s *tui) removing(k string) {
	r := s.row()
	if r == nil {
		return
	}
	if s.tab == "runs" && s.active == 0 && (k == "d" || k == "D") {
		keys := []string{r.Key}
		if len(s.marked) > 0 {
			keys = nil
			for m := range s.marked {
				keys = append(keys, m)
			}
			sort.Strings(keys)
		}
		how := "all"
		if k == "D" {
			how = "latest"
		}
		p, err := s.u.Plan(keys, how)
		if err != nil {
			s.say(err.Error())
			return
		}
		s.box = &tuiBox{title: p.Title, verb: "remove", lines: s.u.Measure(p), danger: true, do: func(string) {
			s.marked = map[string]bool{}
			go s.u.Remove(p)
			s.say("removing…")
		}}
		return
	}
	if s.tab != "caches" {
		return
	}
	switch k {
	case "v":
		s.every = !s.every
	case "n":
		s.box = &tuiBox{title: "unused after how many days", verb: "keep it", prompt: true, text: fmt.Sprintf("%g", s.days), do: func(t string) {
			if _, err := fmt.Sscan(t, &s.days); err != nil || s.days <= 0 {
				s.days = 30
			}
		}}
	case "h":
		w, _ := r.What.(map[string]any)
		if w == nil || s.detailM == nil {
			return
		}
		m := s.detailM
		s.box = &tuiBox{title: "held by " + m.Name() + "?", verb: "hold", lines: []string{"its agent purges it, once a day"}, do: func(string) {
			if _, err := s.u.Ask(m, map[string]any{"op": "caches_hold", "id": w["id"]}); err != nil {
				s.say(kit.FirstLine(err.Error()))
			} else {
				s.say("held by " + m.Name())
			}
		}}
	default:
		title, lines, do, err := s.u.CacheRemoval(r, map[string]string{"i": "dead", "u": "unused", "d": "flagged", "D": "everywhere"}[k], s.days)
		if err != nil {
			s.say(err.Error())
			return
		}
		s.box = &tuiBox{title: title, verb: "remove", lines: lines, danger: true, do: func(string) { s.say(do()) }}
	}
}
