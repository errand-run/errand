package agent

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
)

// quit: this agent told to go -- not while it holds something, unless it is forced: what it holds is then stopped
func (a *Place) quit(req map[string]any, f *reach.FarChan) {
	force, _ := req["force"].(bool)
	after, _ := req["after"].(bool)
	a.mu.Lock()
	deps := a.deps + a.claims
	a.mu.Unlock()
	if n := a.running() + len(a.serving()) + len(a.vms()); (n > 0 || deps > 0) && !force {
		f.Reply(map[string]any{"ok": true, "said": fmt.Sprintf("kept: %d running ( --force stops them )", max(n, deps))})
		return
	}
	a.mu.Lock()
	a.leaving = true // whoever comes from here on is told nothing, and asks again: another agent answers
	a.mu.Unlock()
	f.Reply(map[string]any{"ok": true, "said": "stopped"})
	if again, _ := req["start"].(bool); again { // another in its place, once this one is gone
		cmd := exec.Command(kit.SelfPath(), "place-again", "--dir", a.Dir)
		cmd.SysProcAttr = system.Detached()
		if cmd.Start() == nil {
			_ = cmd.Process.Release()
		}
	}
	go func() {
		if after { // once the command that asks is over
			for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(100 * time.Millisecond) {
				a.mu.Lock()
				busy := a.busy
				a.mu.Unlock()
				if busy == 0 {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
		a.told <- struct{}{}
	}()
}

// ── caches ───────────────────────────────────────────────────────────────────

// cachesHeld: the library caches this machine's runs used -- `purge`: ADR 0020's rules applied first
func cachesHeld(purge bool, dry bool, unused *float64, days float64, entries map[string]bool) []map[string]any {
	var dirs []map[string]any
	for _, rec := range caches.RegisteredCaches() {
		path, _ := rec["path"].(string)
		var those []map[string]any
		for _, cache := range caches.CachesIn(path) {
			var done map[string]any
			if purge {
				done = caches.PurgeCache(cache, caches.Clock(), dry, 10, "", unused)
			}
			s := caches.CacheSummary(cache, days, entries[filepath.Base(cache)] || entries["*"], !dry, caches.Clock())
			if done != nil {
				s["purged_now"] = done
			}
			those = append(those, s)
		}
		rec["caches"] = those
		dirs = append(dirs, rec)
	}
	return dirs
}

// serveCaches: what the two above ask, where the caches are
func serveCaches(req map[string]any, f *reach.FarChan) {
	defer func() {
		if r := recover(); r != nil {
			f.Fail("%s", kit.SaidError(r))
		}
	}()
	if req["op"] == "caches" {
		var unused *float64
		if req["unused"] != nil {
			d := kit.Num(req["unused"], 0)
			unused = &d
		}
		entries := map[string]bool{}
		for _, e := range kit.Strs(req["entries"]) {
			entries[e] = true
		}
		dirs := cachesHeld(caches.Truthy(req["purge"]), caches.Truthy(req["dry_run"]), unused, kit.Num(req["days"], caches.UnusedDays), entries)
		f.Reply(map[string]any{"ok": true, "dirs": dirs, "host": reach.HereName()})
		return
	}
	f.Reply(caches.CachesAnswer(req))
}
