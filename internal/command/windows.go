package command

import (
	"context"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
)

// setupWindows: `errand setup` of what an environment's Windows is -> how it ended
func setupWindows(t *reach.Term, doc *project.Document, name string, env project.Env, force, dry bool) int {
	h := reach.HostOf(env.Stack[0])
	w, _ := env.Stack[0]["windows"].(map[string]any)
	if h.VM != nil {
		l, err := reach.LinkTo(t, h.VM.Via)
		if err != nil {
			t.Sayf("errand: %s: %v\n", name, err)
			return 1
		}
		return followSetup(t, l, map[string]any{"op": "vm_install", "vm": h.VM, "setup": w["setup"], "wanted": doc.Images(), "force": force, "dry_run": dry})
	}
	mark := kit.Text(w["mark"])
	h.Environ = t.Environ
	out, _, code, err := h.Talk(`if exist "`+reach.Backslashed(mark)+`" ( echo errand-ready )`, time.Minute)
	if err != nil || code != 0 {
		t.Sayf("errand: %s: cannot reach %s: `ssh %s echo ok` must work from this terminal, and its shell be cmd.exe\n", name, h.Host, h.Host)
		return 1
	}
	if strings.Contains(out, "errand-ready") && !force {
		return 0
	}
	if dry {
		t.Sayf("  would set up the Windows at %s\n", h.Host)
		return 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-t.Sigs:
			cancel()
		case <-ctx.Done():
		}
	}()
	h.Stop = ctx.Done()
	if err := agent.SetUpBox(h, agent.ReadWindowsSetup(w["setup"]), mark, t.Say); err != nil {
		t.Sayf("  %v\n", err)
		return 1
	}
	return 0
}

// windowsState: how an environment's Windows is, when that says it all -- nothing is booted to be asked
func windowsState(t *reach.Term, env project.Env) string {
	if len(env.Stack) == 0 || env.Stack[0]["windows"] == nil {
		return ""
	}
	h := reach.HostOf(env.Stack[0])
	w, _ := env.Stack[0]["windows"].(map[string]any)
	if h.VM == nil {
		h.Environ = t.Environ
		out, said, code, err := h.Talk(`if exist "`+reach.Backslashed(kit.Text(w["mark"]))+`" ( echo errand-ready )`, time.Minute)
		switch {
		case err != nil || code != 0:
			return "unreachable ( " + kit.FirstLine(kit.EndOf(said, 200)) + " )"
		case !strings.Contains(out, "errand-ready"):
			return "not built"
		}
		return ""
	}
	l, err := reach.LinkTo(t, h.VM.Via)
	if err != nil {
		return "unreachable ( " + kit.FirstLine(err.Error()) + " )"
	}
	got, err := l.Ask(map[string]any{"op": "vm_state", "vm": h.VM})
	if err != nil {
		return "unknown ( " + err.Error() + " )"
	}
	if why := kit.Text(got["missing"]); why != "" {
		return "not built"
	}
	if len(layer.Artefacts(env.Stack[1:])) == 0 {
		return "ok"
	}
	still := h // what is built inside is the guest's to say, and it is not booted to be asked
	still.NoBoot = true
	if _, err := t.MachineUp(&still); err != nil {
		return "unknown ( the machine is not up )"
	}
	return ""
}

// vmMain: `errand vm stop` -- the machines kept on this one, and on those the project's environments boot theirs on
func vmMain(t *reach.Term, args []string) int {
	if len(args) != 1 || args[0] != "stop" {
		t.Sayln("errand: errand vm stop")
		return 2
	}
	at := []*reach.HostSpec{nil}
	seen := map[string]bool{}
	if doc, err := declared(t, ""); err == nil {
		for _, e := range doc.Envs {
			if len(e.Stack) == 0 || e.Stack[0]["windows"] == nil {
				continue
			}
			if h := reach.HostOf(e.Stack[0]); h.VM != nil && h.VM.Via != nil && !seen[h.VM.Via.Key()] {
				seen[h.VM.Via.Key()] = true
				at = append(at, h.VM.Via)
			}
		}
	}
	code, some := 0, false
	for _, h := range at {
		where := "here"
		if h != nil {
			where = h.Host
		}
		l, err := reach.LinkTo(t, h)
		var got map[string]any
		if err == nil {
			got, err = l.Ask(map[string]any{"op": "vm_stop"})
		}
		if err != nil {
			t.Printf("  %s: could not be asked ( %s )\n", where, kit.FirstLine(err.Error()))
			code = 1
			continue
		}
		for _, what := range kit.Strs(got["stopped"]) {
			some = true
			t.Printf("  %s: %s stopped\n", where, what)
		}
	}
	if !some && code == 0 {
		t.Println("  no virtual machine is running")
	}
	return code
}
