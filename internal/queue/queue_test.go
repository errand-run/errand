package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func machine(t *testing.T, cap Capacity) *Queue {
	t.Helper()
	for _, v := range []string{EnvClaim, EnvNoQueue, EnvAllocation, "SLURM_JOB_ID", "PBS_JOBID", "OAR_JOB_ID", "LSB_JOBID", "SGE_TASK_ID", "FLUX_JOB_ID"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	return At(t.TempDir(), cap)
}

var workstation = Capacity{CPUs: 8, RAM: 16384, GPUs: 2, CardRAM: []float64{24000, 24000}}

func take(t *testing.T, q *Queue, r Request) *Granted {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g, err := q.Acquire(ctx, r)
	if err != nil {
		t.Fatalf("%s: %v", r.Label, err)
	}
	t.Cleanup(g.Release)
	return g
}

// waits: the request does not fit; what it is told it waits for
func waits(t *testing.T, q *Queue, r Request) string {
	t.Helper()
	said := ""
	r.Say = func(s string) { said = s }
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	g, err := q.Acquire(ctx, r)
	if err == nil {
		g.Release()
		t.Fatalf("%s was let in, with cards %v", r.Label, g.Devices)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s: %v", r.Label, err)
	}
	return said
}

func TestSizesAndShares(t *testing.T) {
	for text, mb := range map[string]float64{"8G": 8192, "512M": 512, "2048": 2048, "1T": 1024 * 1024, "4gb": 4096, "1024K": 1} {
		if got, err := ParseSize(text); err != nil || got != mb {
			t.Errorf("ParseSize( %q ) = %v, %v; want %v", text, got, err, mb)
		}
	}
	for _, bad := range []string{"", "lots", "-1G", "150%", "0%"} {
		if _, err := ParseShare(bad); err == nil && bad != "" {
			t.Errorf("ParseShare( %q ) was taken", bad)
		}
	}
	s, _ := ParseShare("75%")
	if mb, ok := s.On(24000); !ok || mb != 18000 {
		t.Errorf("75%% of 24000 = %v, %v", mb, ok)
	}
	if _, ok := s.On(0); ok {
		t.Error("a share of a card whose size nobody knows is the card, whole")
	}
	b, _ := json.Marshal(Needs{GPUs: 1, GPURam: s})
	var back Needs
	if err := json.Unmarshal(b, &back); err != nil || back != (Needs{GPUs: 1, GPURam: s}) {
		t.Errorf("%s read back as %+v ( %v )", b, back, err)
	}
	if n := (Needs{GPURam: s}).Normalized(1); n.GPUs != 1 {
		t.Error("memory on a card is a card")
	}
	if n := (Needs{RAM: 100}).Normalized(4); n.CPUs != 4 || n.RAM != 400 {
		t.Errorf("four ranks: %+v", n)
	}
}

func TestSharedUntilFull(t *testing.T) {
	q := machine(t, workstation)
	a := take(t, q, Request{Label: "a", Needs: Needs{CPUs: 6}})
	take(t, q, Request{Label: "slot"}) // what asks for nothing is counted against nothing
	if why := waits(t, q, Request{Label: "b", Needs: Needs{CPUs: 4}}); !strings.Contains(why, "cpus (6 of 8 taken, needs 4)") {
		t.Errorf("told %q", why)
	}
	a.Release()
	take(t, q, Request{Label: "b", Needs: Needs{CPUs: 4}})
	if why := waits(t, q, Request{Label: "c", Needs: Needs{RAM: 20000}}); !strings.Contains(why, "the machine to itself") {
		t.Errorf("more than the machine has, with others running: told %q", why)
	}
}

func TestMoreThanExistsRunsAlone(t *testing.T) {
	q := machine(t, workstation)
	g := take(t, q, Request{Label: "huge", Needs: Needs{CPUs: 64, GPUs: 5}})
	if !reflect.DeepEqual(g.Devices, []int{0, 1}) {
		t.Errorf("given %v", g.Devices)
	}
}

func TestExclusive(t *testing.T) {
	q := machine(t, workstation)
	slot := take(t, q, Request{Label: "make"})
	if why := waits(t, q, Request{Label: "bench", Exclusive: true}); !strings.Contains(why, "the machine to itself (1 running)") {
		t.Errorf("told %q", why)
	}
	slot.Release()
	bench := take(t, q, Request{Label: "bench", Exclusive: true})
	if !reflect.DeepEqual(bench.Devices, []int{0, 1}) {
		t.Errorf("the machine to itself is its cards too: %v", bench.Devices)
	}
	if why := waits(t, q, Request{Label: "make"}); !strings.Contains(why, "bench, which has the machine to itself") {
		t.Errorf("told %q", why)
	}
}

func TestFirstComeFirstServed(t *testing.T) {
	q := machine(t, workstation)
	slot := take(t, q, Request{Label: "long"})
	got := make(chan *Granted, 1)
	go func() { // a benchmark arrives, and waits for the machine
		g, _ := q.Acquire(context.Background(), Request{Label: "bench", Exclusive: true})
		got <- g
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(q.Waiting()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// small work that would fit does not pass it: a steady stream of it would starve the benchmark for ever
	if why := waits(t, q, Request{Label: "small"}); !strings.Contains(why, "bench, which is waiting for the machine to itself") {
		t.Errorf("told %q", why)
	}
	slot.Release()
	select {
	case g := <-got:
		g.Release()
	case <-time.After(3 * time.Second):
		t.Fatal("the benchmark never got the machine")
	}
	if len(q.Waiting()) != 0 || !q.Idle() {
		t.Errorf("left behind: %v", q.Describe())
	}
}

func TestCardsAreAssigned(t *testing.T) {
	q := machine(t, workstation)
	a := take(t, q, Request{Label: "a", Needs: Needs{GPUs: 1}})
	b := take(t, q, Request{Label: "b", Needs: Needs{GPUs: 1}})
	if a.Devices[0] == b.Devices[0] {
		t.Fatalf("two runs told the same card: %v %v", a.Devices, b.Devices)
	}
	if a.Env()["CUDA_VISIBLE_DEVICES"] != "0" || b.HandedDown()[EnvClaim] != "1" || b.HandedDown()[EnvClaimFile] != b.Path {
		t.Errorf("told %v, %v", a.Env(), b.HandedDown())
	}
	if why := waits(t, q, Request{Label: "c", Needs: Needs{GPUs: 1}}); !strings.Contains(why, "gpus (0 free of 2, needs 1)") {
		t.Errorf("told %q", why)
	}
}

func TestCardsShared(t *testing.T) {
	q := machine(t, workstation)
	third, _ := ParseShare("30%")
	most, _ := ParseShare("75%")
	var on [2]int
	for i := 0; i < 6; i++ { // 30 % each: three to a card, on the card with the most left each time
		g := take(t, q, Request{Label: "third", Needs: Needs{GPURam: third}.Normalized(1)})
		on[g.Devices[0]]++
	}
	if on != [2]int{3, 3} {
		t.Errorf("six thirds went %v", on)
	}
	if why := waits(t, q, Request{Label: "seventh", Needs: Needs{GPURam: third}.Normalized(1)}); !strings.Contains(why, "30% of each") {
		t.Errorf("told %q", why)
	}
	q2 := machine(t, workstation)
	take(t, q2, Request{Label: "most", Needs: Needs{GPURam: most}.Normalized(1)})
	g := take(t, q2, Request{Label: "most", Needs: Needs{GPURam: most}.Normalized(1)})
	if c, _ := g.Row(); len(c.CardRAM) != 1 || c.CardRAM[0] != 18000 {
		t.Errorf("its claim says %+v", c.CardRAM)
	}
	if why := waits(t, q2, Request{Label: "whole", Needs: Needs{GPUs: 1}}); why == "" {
		t.Error("a card somebody holds a part of was given whole")
	}
	// no size known: `gpu_ram` takes cards whole
	q3 := machine(t, Capacity{CPUs: 8, GPUs: 1})
	take(t, q3, Request{Label: "a", Needs: Needs{GPURam: third}.Normalized(1)})
	if why := waits(t, q3, Request{Label: "b", Needs: Needs{GPURam: third}.Normalized(1)}); why == "" {
		t.Error("two shares of a card whose size nobody knows")
	}
}

func TestAHolderThatIsGone(t *testing.T) {
	q := machine(t, workstation)
	g := take(t, q, Request{Label: "killed", Exclusive: true})
	q.alive = func(Claim) (bool, bool) { return false, true } // its process is gone: known at once, no heartbeat waited for
	other := take(t, q, Request{Label: "next", Exclusive: true})
	if !g.Lost() || other.Lost() {
		t.Error("the claim of a process that is gone was not given back")
	}
	// one whose process cannot be seen from here ( a container's numbers ) lives as its heartbeat says
	q.alive = func(Claim) (bool, bool) { return false, false }
	if len(q.Held()) != 1 {
		t.Fatal("a fresh claim whose holder cannot be seen was dropped")
	}
	old := time.Now().Add(-2 * StaleAfter)
	_ = os.Chtimes(other.Path, old, old)
	if len(q.Held()) != 0 {
		t.Error("a claim nobody breathes for was kept")
	}
	// ... and one whose process is seen alive keeps it however long it says nothing ( stopped, swapped out )
	q.alive = func(Claim) (bool, bool) { return true, true }
	slow := take(t, q, Request{Label: "slow"})
	_ = os.Chtimes(slow.Path, old, old)
	if len(q.Held()) != 1 {
		t.Error("a holder that is only slow lost its claim")
	}
}

func TestKeptWarmGivesWay(t *testing.T) {
	q := machine(t, workstation)
	warm := q.Try(Request{Label: "zygote", Needs: Needs{GPUs: 2}}, true)
	if warm == nil {
		t.Fatal("nothing held the cards, and they were not given")
	}
	defer warm.Release()
	if q.Try(Request{Label: "another", Needs: Needs{GPUs: 1}}, true) != nil {
		t.Error("what is only kept warm does not make another give way")
	}
	got := make(chan *Granted, 1)
	go func() {
		g, _ := q.Acquire(context.Background(), Request{Label: "train", Needs: Needs{GPUs: 1}})
		got <- g
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !warm.Evicted() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !warm.Evicted() {
		t.Fatal("it was not asked to give way")
	}
	warm.Release()
	select {
	case g := <-got:
		g.Release()
	case <-time.After(3 * time.Second):
		t.Fatal("the room given back was not taken")
	}
	busy := q.Try(Request{Label: "zygote", Needs: Needs{GPUs: 2}}, true)
	busy.SetPreemptible(false) // in use now: it may not be asked
	defer busy.Release()
	if why := waits(t, q, Request{Label: "train", Needs: Needs{GPUs: 1}}); strings.Contains(why, "kept warm") || busy.Evicted() {
		t.Errorf("what is in use was asked to give way: %q", why)
	}
}

// A request that gives up no longer asks anything to give way: what was only kept warm is not stopped for nobody
// ( found on a real machine: a run stopped while it waited left a service asked to go ).
func TestARequestThatGivesUpAsksNothingMore(t *testing.T) {
	q := machine(t, workstation)
	warm := q.Try(Request{Label: "a service", Needs: Needs{GPUs: 2}}, true)
	if warm == nil {
		t.Fatal("nothing held the cards, and they were not given")
	}
	defer warm.Release()
	ctx, cancel := context.WithCancel(context.Background())
	gave := make(chan error, 1)
	go func() {
		_, err := q.Acquire(ctx, Request{Label: "train", Needs: Needs{GPUs: 1}})
		gave <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !warm.Evicted() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !warm.Evicted() {
		t.Fatal("it was not asked to give way")
	}
	cancel()
	if err := <-gave; err == nil {
		t.Fatal("a request that was stopped was granted")
	}
	if warm.Evicted() {
		t.Error("still asked to give way, by a request that gave up")
	}
}

func TestInheritedAndScheduled(t *testing.T) {
	q := machine(t, workstation)
	take(t, q, Request{Label: "parent", Exclusive: true})
	t.Setenv(EnvClaim, "1")
	g := take(t, q, Request{Label: "child", Exclusive: true}) // would wait for ever for what its own parent holds
	if g.Path != "" || !reflect.DeepEqual(g.Devices, []int{1}) {
		t.Errorf("a child of a claim: %+v", g)
	}
	os.Unsetenv(EnvClaim)
	t.Setenv("SLURM_JOB_ID", "42")
	said := ""
	g, _ = q.Acquire(context.Background(), Request{Label: "in a job", Say: func(s string) { said = s }})
	if g.Path != "" || !strings.Contains(said, "slurm already decided") || len(g.Devices) != 2 {
		t.Errorf("in somebody's allocation: %+v, told %q", g, said)
	}
	t.Setenv(EnvAllocation, "42") // ... which errand holds: carved by its own queue
	if Scheduler() != "" || !strings.HasSuffix(Dir(), "-alloc42") {
		t.Errorf("in an allocation errand holds: scheduler %q, queue %s", Scheduler(), Dir())
	}
}

func TestAClaimGrows(t *testing.T) {
	q := machine(t, workstation)
	ctx := context.Background()
	run := take(t, q, Request{Label: "train", Needs: Needs{CPUs: 2}})
	got, err := q.Grow(ctx, run.Path, Growth{Needs: Needs{GPUs: 1, RAM: 4096}})
	if err != nil || len(got) != 1 {
		t.Fatalf("grown to %v, %v", got, err)
	}
	c, _ := run.Row()
	if c.Needs != (Needs{CPUs: 2, RAM: 4096, GPUs: 1}) || *c.Base != (Needs{CPUs: 2}) || *c.Wanted != c.Needs || c.PID != os.Getpid() {
		t.Errorf("its claim: %+v, base %+v, wanted %+v", c.Needs, c.Base, c.Wanted)
	}
	// "at least that much in all": asked again, or for less, nothing changes
	if again, _ := q.Grow(ctx, run.Path, Growth{Needs: Needs{GPUs: 1, RAM: 1024}}); !reflect.DeepEqual(again, got) {
		t.Errorf("asked again: %v", again)
	}
	// on top of what it was started with: several add up
	for i := 0; i < 2; i++ {
		if _, err := q.Grow(ctx, run.Path, Growth{Needs: Needs{CPUs: 1}, Extra: true}); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ = run.Row(); c.CPUs != 4 || c.RequiredExtra.CPUs != 2 || c.Required.GPUs != 1 {
		t.Errorf("after two more cores: %+v ( required %+v, extra %+v )", c.Needs, c.Required, c.RequiredExtra)
	}
	// it waits for what another holds, and gets it when that one goes; a new request does not pass it
	other := take(t, q, Request{Label: "other", Needs: Needs{GPUs: 1}})
	grown := make(chan []int, 1)
	go func() {
		got, _ := q.Grow(ctx, run.Path, Growth{Needs: Needs{GPUs: 2}})
		grown <- got
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(q.Waiting()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if why := waits(t, q, Request{Label: "new"}); !strings.Contains(why, "train, which is waiting for more than it holds") {
		t.Errorf("told %q", why)
	}
	other.Release()
	select {
	case cards := <-grown:
		if len(cards) != 2 || cards[0] != got[0] {
			t.Errorf("grown to cards %v: the one it had, first ( %v )", cards, got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("it never grew")
	}
	run.Release()
	if _, err := q.Grow(ctx, run.Path, Growth{Needs: Needs{CPUs: 1}}); !errors.Is(err, ErrGone) {
		t.Errorf("a claim that is gone: %v", err)
	}
}

func TestTwoThatGrowIntoEachOther(t *testing.T) {
	q := machine(t, workstation)
	older := take(t, q, Request{Label: "older", Needs: Needs{GPUs: 1}})
	time.Sleep(5 * time.Millisecond)
	younger := take(t, q, Request{Label: "younger", Needs: Needs{GPUs: 1}})
	first := make(chan error, 1)
	go func() { // each wants the card the other holds
		_, err := q.Grow(context.Background(), older.Path, Growth{Needs: Needs{GPUs: 2}})
		first <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(q.Waiting()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := q.Grow(ctx, younger.Path, Growth{Needs: Needs{GPUs: 2}}); !errors.Is(err, ErrRerun) {
		t.Fatalf("the younger of the two: %v", err)
	}
	if c, _ := younger.Row(); !c.Rerun || c.Wanted.GPUs != 2 {
		t.Errorf("what it is run again with: %+v", c)
	}
	younger.Release() // stopped, to be run again with two cards up front
	select {
	case err := <-first:
		if err != nil {
			t.Errorf("the older one: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the older one never grew")
	}
}

func TestMergedAndAdded(t *testing.T) {
	pct := func(p float64) Share { return Share{Percent: p} }
	mb := func(m float64) Share { return Share{MB: m} }
	for _, c := range []struct {
		name       string
		held, more Needs
		size       float64
		merged     Share
		added      Share
	}{
		{"two shares", Needs{GPUs: 1, GPURam: pct(30)}, Needs{GPURam: pct(50)}, 24000, pct(50), pct(80)},
		{"shares past the card", Needs{GPUs: 1, GPURam: pct(70)}, Needs{GPURam: pct(50)}, 24000, pct(70), pct(100)},
		{"two sizes", Needs{GPUs: 1, GPURam: mb(4000)}, Needs{GPURam: mb(6000)}, 24000, mb(6000), mb(10000)},
		{"a share and a size", Needs{GPUs: 1, GPURam: pct(25)}, Needs{GPURam: mb(8000)}, 24000, mb(8000), mb(14000)},
		{"... on a card nobody knows the size of", Needs{GPUs: 1, GPURam: pct(25)}, Needs{GPURam: mb(8000)}, 0, pct(25), Share{}},
		{"cards held whole stay whole", Needs{GPUs: 1}, Needs{GPURam: pct(50)}, 24000, Share{}, Share{}},
	} {
		if got := merged(c.held, c.more, c.size).GPURam; got != c.merged {
			t.Errorf("%s: merged to %v, want %v", c.name, got, c.merged)
		}
		if got := added(c.held, c.more, c.size).GPURam; got != c.added {
			t.Errorf("%s: added to %v, want %v", c.name, got, c.added)
		}
	}
}

func TestDescribe(t *testing.T) {
	q := machine(t, workstation)
	take(t, q, Request{Label: "solve", Needs: Needs{CPUs: 4, RAM: 2048, GPUs: 1}})
	lines := q.Describe()
	if len(lines) != 1 || !strings.Contains(lines[0], "solve  cpus=4, ram=2048, gpus=1  gpu 0") {
		t.Errorf("%q", lines)
	}
}
