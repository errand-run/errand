package reach

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// The machines of the ledger: one per machine, told by what tells it from any other, else by its names.
func TestTheLedgerTellsMachinesApart(t *testing.T) {
	cases := []struct {
		name  string
		notes []MachineNote
		want  []string // the machines' names, sorted
	}{
		{"one hostname, two machines", []MachineNote{
			{Host: "node14", Options: []string{"-J", "cinaps"}, Hostname: "node14", ID: "a"},
			{Host: "node14", Options: []string{"-J", "jz"}, Hostname: "node14", ID: "b"}},
			[]string{"node14", "node14 ( -J jz )"}},
		{"two ways to one machine", []MachineNote{
			{Host: "lmo", Hostname: "leclerc-tower", ID: "a"},
			{Host: "hugo@lmo-vpn", Hostname: "leclerc-tower", ID: "a"}},
			[]string{"lmo"}},
		{"one ssh name, two machines behind it", []MachineNote{
			{Host: "cinaps", Hostname: "cinaps1", ID: "a"},
			{Host: "hugo@cinaps", Hostname: "cinaps2", ID: "b"}},
			[]string{"cinaps", "hugo@cinaps"}},
		{"an errand that did not say it: the hostname", []MachineNote{
			{Host: "node14", Options: []string{"-J", "cinaps"}, Hostname: "node14", ID: "a"},
			{Host: "n14", Hostname: "node14"}},
			[]string{"node14"}},
		{"one label, two machines", []MachineNote{
			{Host: "lmo", Hostname: "tower", ID: "a", Label: "gpu"},
			{Host: "jz", Hostname: "jean-zay1", ID: "b", Label: "gpu"}},
			[]string{"gpu", "gpu ( jz )"}},
		{"two places of errand's on one machine", []MachineNote{
			{Host: "lmo", Hostname: "tower", ID: "a"},
			{Host: "lmo", Hostname: "tower", ID: "a", Cache: "/scratch/errand"}},
			[]string{"lmo", "lmo ( /scratch/errand )"}},
		{"not this machine's name", []MachineNote{
			{Host: "here-too", Hostname: "x", ID: "a", Label: "mac"}},
			[]string{"mac ( here-too )"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ERRAND_CACHE_DIR", t.TempDir())
			now := kit.Seconds(time.Now())
			for i, n := range c.notes {
				n.V, n.First, n.Used = 1, now+float64(i), now-float64(i)
				n.Write()
			}
			ms := Ledger("mac")
			var got []string
			for _, m := range ms[1:] {
				got = append(got, m.Name)
			}
			sort.Strings(got)
			if strings.Join(got, " | ") != strings.Join(c.want, " | ") {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// What a machine answered is kept: its id is not lost when an errand that does not say it comes after.
func TestTheIdIsKept(t *testing.T) {
	t.Setenv("ERRAND_CACHE_DIR", t.TempDir())
	h := HostSpec{Host: "lmo"}
	UsedMachine(h, "tower", "a")
	UsedMachine(h, "tower", "")
	if ms := Ledger("mac"); len(ms) != 2 || ms[1].Notes[0].ID != "a" {
		t.Errorf("%+v", ms)
	}
}
