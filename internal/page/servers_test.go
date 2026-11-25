package page

import (
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// The servers held are with what runs; the machines say those that rest too, which nothing is asked to stop.
func TestTheServersAreShown(t *testing.T) {
	now := kit.Seconds(time.Now())
	m := &UiMachine{M: reach.Machine{Name: "lmo"}, readAt: time.Now(), doc: &reach.OverviewDoc{Now: now,
		Serving: []reach.ServingRow{{Name: "ollama", State: "in use", Started: now - 60}},
		Rested:  []reach.ServingRow{{Name: "viewer", State: "stopped", Ended: now - 60}}}}
	u := &Ui{machines: []*UiMachine{m}, ReadAt: time.Now()}
	count := func(rows []UiRow) map[string]int {
		out := map[string]int{}
		for _, r := range rows {
			out[r.Kind]++
			if _, is := r.What.(*reach.ServingRow); is && (len(u.KillList(&r)) > 0) != (r.Kind == "service") {
				t.Errorf("%s: stopped or not as it should: %v", r.Label, u.KillList(&r))
			}
		}
		return out
	}
	if got := count(u.serverRows(UiAsked{})); got["service"] != 1 || got["rested"] != 0 || got["day"] != 1 {
		t.Errorf("runs: %v", got)
	}
	if got := count(u.serverRows(UiAsked{Folded: map[string]bool{"day:servers": true}})); got["service"] != 0 || got["day"] != 1 {
		t.Errorf("runs, folded: %v", got)
	}
	if got := u.serverRows(UiAsked{Project: "/elsewhere"}); got != nil {
		t.Errorf("another project's: %v", got)
	}
	if got := count(u.machineRows(UiAsked{})); got["service"] != 1 || got["rested"] != 1 {
		t.Errorf("machines: %v", got)
	}
}
