package reach

import (
	"os"
	"path/filepath"
	"time"

	"github.com/errand-run/errand/internal/queue"
)

// vmImageDays: an evaluation lasts 180 days, less a little: past it, Windows stops by itself every hour
const vmImageDays = 170

func (w VmWant) Dir() string {
	if filepath.IsAbs(filepath.FromSlash(w.Image)) {
		return filepath.FromSlash(w.Image)
	}
	return filepath.Join(CopyRoot(w.Root), filepath.FromSlash(w.Image))
}

// Installed: why the image cannot be booted; "" when it can
func (w VmWant) Installed() string {
	st, err := os.Stat(filepath.Join(w.Dir(), "ready"))
	switch {
	case err != nil:
		return "windows is not installed yet on " + queue.Host()
	case !w.Keyed && time.Since(st.ModTime()) > vmImageDays*24*time.Hour:
		return "the windows installed on " + queue.Host() + " is an evaluation about to run out: it is to be installed again"
	}
	return ""
}

// ── from the command ─────────────────────────────────────────────────────────

// MachineUp: a machine errand boots, up and held for as long as the command lasts -> the way to its agent
func (t *Term) MachineUp(h *HostSpec) (*Link, error) {
	t.vmMu.Lock()
	defer t.vmMu.Unlock()
	if l := t.machines[h.Key()]; l != nil {
		select {
		case <-l.Dead:
		default:
			return l, nil
		}
	}
	base, err := LinkTo(t, h.VM.Via)
	if err != nil {
		return nil, err
	}
	if _, err := AskHeld(t, base, map[string]any{"op": "vm_hold", "vm": h.VM, "boot": !h.NoBoot}, false, &t.held); err != nil {
		return nil, err
	}
	if h.VM.Via != nil {
		UsedMachine(*h.VM.Via, "")
	}
	want := h.VM
	l := &Link{base: base, Dead: base.Dead, Where: h.Host, wrap: func(req any) any {
		return map[string]any{"op": "vm_in", "vm": want, "req": req}
	}}
	if t.machines == nil {
		t.machines = map[string]*Link{}
	}
	t.machines[h.Key()] = l
	return l, nil
}
