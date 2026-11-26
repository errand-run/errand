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

// A job handed to Slurm from cinaps, running on node14: node14 says it when it is asked, cinaps otherwise.
func TestAJobIsSaidWhereItRuns(t *testing.T) {
	login := func() *OverviewDoc {
		return &OverviewDoc{Host: "cinaps2", Rows: []TableRow{{ID: "r1", State: "running", Slurm: "50029"}, {ID: "r2", State: "running"}},
			Kept: []KeptRow{{Kind: "allocation", ID: "50029", On: "node14"}, {Kind: "allocation", ID: "50030"}}}
	}
	node := &OverviewDoc{Host: "node14", Rows: []TableRow{{ID: "r1", State: "running"}}, Kept: []KeptRow{{Kind: "allocation", ID: "50029", On: "node14"}}}
	got := Elsewhere([]*OverviewDoc{login(), node, nil})
	if want := map[string]bool{"run:r1": true, "alloc:50029": true}; len(got[0]) != 2 || !got[0]["run:r1"] || !got[0]["alloc:50029"] {
		t.Errorf("the login node: %v, want %v", got[0], want)
	}
	if len(got[1]) != 0 || len(got[2]) != 0 {
		t.Errorf("the node: %v; nothing: %v", got[1], got[2])
	}
	if got := Elsewhere([]*OverviewDoc{login(), nil}); len(got[0]) != 0 {
		t.Errorf("its node not asked: the login node says it all, %v", got[0])
	}
	said := Once([]machineSaid{{Doc: *login()}, {Doc: *node}})
	if len(said[0].Doc.Rows) != 1 || said[0].Doc.Rows[0].ID != "r2" || len(said[0].Doc.Kept) != 1 || len(said[1].Doc.Rows) != 1 {
		t.Errorf("%+v", said)
	}
}
