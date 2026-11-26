package page

import (
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// A machine renamed on the page is shown by that name, and found by it.
func TestAMachineIsRenamed(t *testing.T) {
	t.Setenv("ERRAND_CACHE_DIR", t.TempDir())
	old := kit.Seconds(time.Now().Add(-48 * time.Hour)) // dormant: nothing is asked of it
	reach.MachineNote{V: 1, Host: "lmo", Hostname: "leclerc-Precision", First: old, Used: old, Watch: "off"}.Write()
	here := &UiMachine{M: reach.Machine{Name: reach.HereName()}, watching: true, stop: make(chan struct{})}
	u := &Ui{machines: []*UiMachine{here}, Changed: func(bool, []string) {}, Said: func(string) {}}
	u.refresh()
	w := &webServer{}
	key := rowKey("dormant", "lmo", "")
	got, err := w.act(u, map[string]any{"op": "rename", "key": key})
	if err != nil || got["prompt"] == nil {
		t.Fatalf("asked first: %v %v", got, err)
	}
	if _, err = w.act(u, map[string]any{"op": "rename", "key": key, "label": " gpu "}); err != nil {
		t.Fatal(err)
	}
	ms := reach.Ledger(reach.HereName())
	if len(ms) != 2 || ms[1].Name != "gpu" || !ms[1].Named("lmo") {
		t.Fatalf("the ledger: %+v", ms)
	}
	found := false
	for _, r := range u.machineRows(UiAsked{}) {
		found = found || r.Label == "gpu"
	}
	if !found {
		t.Errorf("the page does not show it renamed: %+v", u.machineRows(UiAsked{}))
	}
	// this machine: not offered, and refused before a name is typed
	hereKey := rowKey("machine", reach.HereName(), "")
	if got, err := w.act(u, map[string]any{"op": "rename", "key": hereKey}); err == nil {
		t.Errorf("this machine asked for a name: %v", got)
	}
	if row, err := u.find(hereKey); err != nil || u.can(row, u.machineOf(row))["rename"] != nil {
		t.Errorf("rename offered on this machine: %v", err)
	}
	// a name another machine has: the two would be one
	reach.MachineNote{V: 1, Host: "cinaps", Hostname: "cinaps2", First: old, Used: old, Watch: "off"}.Write()
	u.ReadAt = time.Time{}
	u.refresh()
	if _, err := w.act(u, map[string]any{"op": "rename", "key": rowKey("dormant", "cinaps", ""), "label": "gpu"}); err == nil {
		t.Error("a second machine named gpu")
	}
	if ms := reach.Ledger(reach.HereName()); len(ms) != 3 {
		t.Errorf("the ledger: %+v", ms)
	}
}

// The page: a Slurm job under the node it runs on, not under the login node that handed it, which is not counted busy.
func TestAJobIsShownOnItsNode(t *testing.T) {
	now := kit.Seconds(time.Now())
	login := &UiMachine{M: reach.Machine{Name: "cinaps"}, readAt: time.Now(), doc: &reach.OverviewDoc{Host: "cinaps2", Now: now,
		Rows: []reach.TableRow{{ID: "r1", What: "solve.py", State: "running", Slurm: "50029", Started: now}},
		Kept: []reach.KeptRow{{Kind: "allocation", ID: "50029", On: "node14", Text: "slurm job 50029 running on node14"}}}}
	node := &UiMachine{M: reach.Machine{Name: "node14"}, readAt: time.Now(), doc: &reach.OverviewDoc{Host: "node14", Now: now,
		Rows: []reach.TableRow{{ID: "r1", What: "solve.py", State: "running", Started: now}},
		Kept: []reach.KeptRow{{Kind: "allocation", ID: "50029", On: "node14", Text: "slurm job 50029 running on node14"}}}}
	u := &Ui{machines: []*UiMachine{login, node}, ReadAt: time.Now()}
	said := map[string]string{}
	notes := map[string]int{}
	for _, r := range u.machineRows(UiAsked{}) {
		if r.Kind == "machine" {
			said[*r.Machine] = r.Detail
		}
		if r.Kind == "note" {
			notes[*r.Machine]++
		}
	}
	if said["cinaps"] != "0/0" || said["node14"] != "1/0" || notes["cinaps"] != 0 || notes["node14"] != 1 {
		t.Errorf("counts %v, allocations %v", said, notes)
	}
	procs := 0
	for _, r := range u.runRows(UiAsked{}) {
		if r.Kind == "proc" {
			procs++
			if *r.Machine != "node14" {
				t.Errorf("shown on %s", *r.Machine)
			}
		}
	}
	if procs != 1 {
		t.Errorf("%d rows of it", procs)
	}
}
