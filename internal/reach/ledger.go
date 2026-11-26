// The machines asked what runs there ( the guide's "The machines asked" ): those work was sent to from here, each a file
// under `<cache>/machines/`, kept two weeks after its last use. One reached under several names -- the same ssh name
// but for the user, the same label, or the same hostname answered -- is one. A machine no work was sent to for a day is
// dormant: nothing contacts it unless it is watched.

package reach

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// ── asking them ──────────────────────────────────────────────────────────────

// machineSaid is what a machine said of itself, or why it says nothing.
type machineSaid struct {
	M    Machine
	Doc  OverviewDoc
	Err  error
	None bool // dormant: not asked
}

// look: what there is on a machine -- through the link that is kept to it, else by its agents that are there: none is
// started to answer
func (m Machine) look(t *Term) (OverviewDoc, error) {
	var doc OverviewDoc
	if m.Here() {
		l, err := LinkTo(t, nil)
		if err != nil {
			return doc, err
		}
		got, err := l.Ask(map[string]any{"op": "overview"})
		if err != nil {
			return doc, err
		}
		return doc, DecodeInto(got["overview"], &doc)
	}
	for _, n := range m.Notes {
		h := n.Spec()
		linksMu.Lock()
		l := links[h.Key()]
		linksMu.Unlock()
		if l == nil {
			continue
		}
		select {
		case <-l.Dead:
		default:
			got, err := l.Ask(map[string]any{"op": "overview"})
			if err == nil {
				return doc, DecodeInto(got["overview"], &doc)
			}
		}
	}
	h := *m.Spec()
	h.Environ = t.Environ
	out, err := h.There("peek")
	if err != nil {
		return doc, err
	}
	if len(out) == 0 {
		return OverviewDoc{V: 1, Host: m.Name}, nil // no errand was ever sent there
	}
	if at := strings.IndexByte(string(out), '{'); at > 0 {
		out = out[at:]
	}
	return doc, json.Unmarshal(out, &doc)
}

// There: a verb of the errand that was sent to a machine, run there -> what it printed; nothing when none was sent
func (h HostSpec) There(words ...string) ([]byte, error) {
	info := h.known()
	if info == nil {
		var err error
		if info, err = h.probe(); err != nil {
			return nil, err
		}
	}
	program := ""
	if local, err := h.programFor(info.OS, info.Arch); err == nil {
		if sum, err := kit.HashOfFile(local); err == nil {
			program = info.Cache + "/bin/" + sum + "/errand"
		}
	}
	// this errand, else the newest of those that were sent there
	line := h.instanceSaid(info.Cache) + "p=" + ShellQuote(program) + "; [ -x \"$p\" ] || p=$(ls -t " + ShellQuote(info.Cache+"/bin") + "/*/errand 2>/dev/null | head -1); [ -x \"$p\" ] || exit 0; exec \"$p\""
	for _, w := range words {
		line += " " + ShellQuote(w)
	}
	cmd := h.command(append(h.argv(), "$SHELL -ic "+ShellQuote("sh -c "+ShellQuote(line))))
	var out, said strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &said
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if why := h.whyNot(cmd.ProcessState.ExitCode(), said.String()); why != "" {
				return nil, fmt.Errorf("%s", why)
			}
			return nil, fmt.Errorf("%s", kit.EndOf(said.String(), 200))
		}
	case <-time.After(40 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("nothing came back for 40 s")
	}
	return []byte(out.String()), nil
}

// Gather: what every machine says, this one first; `all`: dormant ones too
func Gather(t *Term, all bool) []machineSaid {
	ms := Ledger(HereName())
	out := make([]machineSaid, len(ms))
	var wg sync.WaitGroup
	for i, m := range ms {
		out[i] = machineSaid{M: m}
		if m.Dormant() && !all {
			out[i].None = true
			continue
		}
		wg.Add(1)
		go func(i int, m Machine) {
			defer wg.Done()
			out[i].Doc, out[i].Err = m.look(t)
		}(i, m)
	}
	wg.Wait()
	return out
}

// Once: what the machines said, a run or an allocation said by the machine it runs on only ( Elsewhere )
func Once(said []machineSaid) []machineSaid {
	docs := make([]*OverviewDoc, len(said))
	for i := range said {
		if !said[i].None && said[i].Err == nil {
			docs[i] = &said[i].Doc
		}
	}
	for i, skip := range Elsewhere(docs) {
		if len(skip) == 0 {
			continue
		}
		d := &said[i].Doc
		rows, kept := []TableRow{}, []KeptRow{}
		for _, r := range d.Rows {
			if !skip["run:"+r.ID] {
				rows = append(rows, r)
			}
		}
		for _, k := range d.Kept {
			if k.Kind != "allocation" || !skip["alloc:"+k.ID] {
				kept = append(kept, k)
			}
		}
		d.Rows, d.Kept = rows, kept
	}
	return said
}

// MachinesMain: `errand machines [forget|rename|watch|gauges]`
func MachinesMain(t *Term, args []string) int {
	ms := Ledger(HereName())
	find := func(word string) (Machine, bool) {
		for _, m := range ms {
			if m.Named(word) {
				return m, true
			}
		}
		t.Sayf("errand machines: %s: no such machine ( `errand machines` lists them )", word)
		return Machine{}, false
	}
	if len(args) == 0 {
		for _, m := range ms[1:] {
			said := "work sent " + Ago(m.Used()) + " ago"
			if m.Dormant() {
				said += ", dormant"
			}
			if w := m.watch(); w != "" {
				said += ", watch " + w
			}
			if !m.Gauges() {
				said += ", no gauges"
			}
			var names []string
			for _, n := range m.Notes {
				if n.Host != m.Name {
					names = append(names, n.Host)
				}
			}
			also := ""
			if len(names) > 0 {
				also = "  ( " + strings.Join(names, ", ") + " )"
			}
			t.Printf("  %-20s %s%s\n", m.Name, said, also)
		}
		if len(ms) == 1 {
			t.Println("  no machine: no work was sent anywhere from here")
		}
		return 0
	}
	if len(args) < 2 {
		t.Sayln("errand machines [forget NAME… | rename NAME LABEL | watch NAME on|off|auto | gauges NAME on|off]")
		return 2
	}
	edit := func(m Machine, how func(n *MachineNote)) {
		for _, n := range m.Notes {
			how(&n)
			n.Write()
		}
	}
	switch args[0] {
	case "forget":
		for _, word := range args[1:] {
			m, ok := find(word)
			if !ok {
				return 1
			}
			for _, n := range m.Notes {
				_ = os.Remove(n.Path())
			}
			t.Printf("  %s  not asked any more, until work is sent there again\n", m.Name)
		}
		return 0
	case "rename", "watch", "gauges":
		m, ok := find(args[1])
		if !ok {
			return 1
		}
		if len(args) != 3 || m.Here() {
			t.Sayf("errand machines %s NAME VALUE, for a machine that is not this one", args[0])
			return 2
		}
		v := args[2]
		switch {
		case args[0] == "rename":
			if err := m.CanBeNamed(v); err != nil {
				t.Sayf("errand machines rename: %s", err)
				return 1
			}
			edit(m, func(n *MachineNote) { n.Label = v })
		case args[0] == "watch" && (v == "on" || v == "off" || v == "auto"):
			if v == "auto" {
				v = ""
			}
			edit(m, func(n *MachineNote) { n.Watch = v })
		case args[0] == "gauges" && (v == "on" || v == "off"):
			edit(m, func(n *MachineNote) { n.NoGauges = v == "off" })
		default:
			t.Sayf("errand machines %s: %s: on, off%s", args[0], v, map[bool]string{true: " or auto", false: ""}[args[0] == "watch"])
			return 2
		}
		return 0
	}
	t.Sayf("errand machines: %s: forget, rename, watch or gauges", args[0])
	return 2
}
