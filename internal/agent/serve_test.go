package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/errand-run/errand/internal/reach"
)

// A service that ran and does not now is said from its directory, whichever agent held it.
func TestAServiceThatRestsIsSaid(t *testing.T) {
	t.Setenv("ERRAND_CACHE_DIR", t.TempDir())
	write := func(name, file, text string) {
		p := filepath.Join(os.Getenv("ERRAND_CACHE_DIR"), "serving", name, file)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("idle", "keeper.log", "2026-11-25 14:00:00 errand: starting x\nhello\n2026-11-25 14:25:43 errand: stopped: unused for 2 min\n")
	write("idle", "ports.json", `{"port":11435}`)
	write("asked", "keeper.log", "2026-11-25 14:25:43 errand: stopped\nwhat the server wrote last\n")
	write("bad", "keeper.log", "2026-11-25 14:25:43 errand: failed: no such program\n")
	write("held", "keeper.log", "2026-11-25 14:25:43 errand: stopped\n")
	write("up", "keeper.log", "2026-11-25 14:25:43 errand: starting x\n")
	write("up", "server.json", `{"pid":1}`) // its server may still be there: no word of it
	got := rested([]reach.ServingRow{{Name: "held"}})
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	for i, want := range []reach.ServingRow{{Name: "asked", State: "stopped"}, {Name: "bad", State: "failed", More: "no such program"}, {Name: "idle", State: "stopped", More: "unused for 2 min"}} {
		if g := got[i]; g.Name != want.Name || g.State != want.State || g.More != want.More || g.Ended == 0 {
			t.Errorf("%+v, not %+v", g, want)
		}
	}
	if p := got[2].Ports; len(p) != 1 || p[0].Port != 11435 {
		t.Errorf("the port kept for it: %+v", p)
	}
}
