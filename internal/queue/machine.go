package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/system"
)

// ── who holds a claim ────────────────────────────────────────────────────────

// processStart: when `pid` started, as a token that differs for a pid reused -- "" where nothing says cheaply
// ( Linux says: field 22 of /proc/<pid>/stat )
func processStart(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(data)
	if i := strings.LastIndex(s, ")"); i >= 0 {
		if f := strings.Fields(s[i+1:]); len(f) > 19 {
			return f[19]
		}
	}
	return ""
}

// pidNamespace: where this process's pids mean something -- a container may have its own numbers, and a pid of
// another namespace says nothing here. "" where there are no such things.
func pidNamespace() string {
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return ns
}

// holderAlive: whether the process that holds `c` is alive, and whether that can be told from here. A process that
// is gone is known to be; one that is there is known to be the holder only where its start can be read.
func holderAlive(c Claim) (alive, known bool) {
	if c.PID <= 0 || c.NS != pidNamespace() {
		return false, false
	}
	if !system.Alive(c.PID) {
		return false, true
	}
	if c.Start == "" {
		return true, false // ( there, or its pid given to another: its heartbeat says )
	}
	return processStart(c.PID) == c.Start, true
}

// ── what the machine has ─────────────────────────────────────────────────────

func run(name string, args ...string) (string, bool) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
		return string(out), err == nil
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", false
	}
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func totalRAM() float64 {
	// an allocation's memory, in one: what Slurm gave it on this node
	if os.Getenv(EnvAllocation) != "" {
		if mb, err := strconv.ParseFloat(os.Getenv("SLURM_MEM_PER_NODE"), 64); err == nil && mb > 0 {
			return mb
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
				kb, _ := strconv.ParseFloat(f[1], 64)
				return kb / 1024
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if out, ok := run("/usr/sbin/sysctl", "-n", "hw.memsize"); ok {
			b, _ := strconv.ParseFloat(strings.TrimSpace(out), 64)
			return b / (1024 * 1024)
		}
	}
	return 0
}

// visibleCards: what `CUDA_VISIBLE_DEVICES` says, and whether it is set. It renumbers from zero for whoever it is
// set on: a process given cards 2 and 5 sees 0 and 1, and must hand 0 and 1 down.
func visibleCards() ([]string, bool) {
	v, set := os.LookupEnv("CUDA_VISIBLE_DEVICES")
	if !set {
		return nil, false
	}
	var ids []string
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, true
}

func cardSizes() []float64 {
	var sizes []float64 // MiB, in the machine's order
	if out, ok := run("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits"); ok {
		for _, l := range lines(out) {
			if mb, err := strconv.ParseFloat(l, 64); err == nil {
				sizes = append(sizes, mb)
			}
		}
	}
	if len(sizes) == 0 {
		if out, ok := run("rocm-smi", "--showmeminfo", "vram", "--json"); ok {
			cards := map[string]map[string]any{}
			if json.Unmarshal([]byte(out), &cards) == nil {
				var names []string
				for k := range cards {
					if strings.HasPrefix(k, "card") {
						names = append(names, k)
					}
				}
				sort.Slice(names, func(i, j int) bool {
					a, _ := strconv.Atoi(names[i][4:])
					b, _ := strconv.Atoi(names[j][4:])
					return a < b
				})
				for _, name := range names {
					total := 0.0
					for k, v := range cards[name] {
						if strings.Contains(k, "Total") && !strings.Contains(k, "Used") {
							total, _ = strconv.ParseFloat(fmt.Sprint(v), 64)
						}
					}
					sizes = append(sizes, total/(1024*1024))
				}
			}
		}
	}
	ids, narrowed := visibleCards()
	if len(sizes) == 0 || !narrowed {
		return sizes
	}
	out := make([]float64, 0, len(ids))
	for i, id := range ids {
		if n, err := strconv.Atoi(id); err == nil {
			if n < len(sizes) {
				out = append(out, sizes[n])
			} else {
				out = append(out, 0)
			}
		} else if i < len(sizes) { // by UUID: in order, the best there is
			out = append(out, sizes[i])
		}
	}
	return out
}

var (
	machineOnce sync.Once
	machineCap  Capacity
)

// Machine: what this process may use of this machine -- the cores ( an allocation's, in one ), the memory, the
// cards somebody upstream left visible. Measured once.
func Machine() Capacity {
	machineOnce.Do(func() {
		c := Capacity{CPUs: float64(runtime.NumCPU()), RAM: totalRAM()}
		if ids, narrowed := visibleCards(); narrowed {
			c.GPUs = len(ids)
		} else if out, ok := run("nvidia-smi", "-L"); ok {
			c.GPUs = len(lines(out))
		}
		if c.GPUs > 0 {
			c.CardRAM = cardSizes()
		}
		machineCap = c
	})
	return machineCap
}
