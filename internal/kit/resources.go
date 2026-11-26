// What a machine has and how much of it is taken: its cores, its memory, its cards.

package kit

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/system"
)

// ── the machine's resources ──────────────────────────

var (
	resMu     sync.Mutex
	lastStat  [2]float64
	haveStat  bool
	gpusAt    time.Time
	gpusCache = []any{}
)

func cpuTimes() (float64, float64, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	f := strings.Fields(line)
	if len(f) < 5 {
		return 0, 0, false
	}
	var parts []float64
	for _, x := range f[1:] {
		v, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, 0, false
		}
		parts = append(parts, v)
	}
	total := 0.0
	for _, v := range parts {
		total += v
	}
	idle := parts[3]
	if len(parts) > 4 {
		idle += parts[4]
	}
	return total, total - idle, true
}

func cpuBusy() any {
	resMu.Lock()
	defer resMu.Unlock()
	total, used, ok := cpuTimes()
	if !ok {
		return nil
	}
	before := lastStat
	if !haveStat {
		before = [2]float64{total, used}
		time.Sleep(100 * time.Millisecond) // asked once: two readings, apart
		total, used, _ = cpuTimes()
	}
	lastStat, haveStat = [2]float64{total, used}, true
	if dt := total - before[0]; dt > 0 {
		b := (used - before[1]) / dt
		if b < 0 {
			b = 0
		}
		if b > 1 {
			b = 1
		}
		return b
	}
	return nil
}

func cmdOut(timeout time.Duration, name string, args ...string) (string, bool) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if cmd.Start() != nil {
		return "", false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err == nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", false
	}
}

func ramNow() any {
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		info := map[string]float64{}
		for _, l := range strings.Split(string(data), "\n") {
			if i := strings.Index(l, ":"); i > 0 {
				if f := strings.Fields(l[i+1:]); len(f) > 0 {
					v, _ := strconv.ParseFloat(f[0], 64)
					info[l[:i]] = v
				}
			}
		}
		if total, ok := info["MemTotal"]; ok {
			free, ok := info["MemAvailable"]
			if !ok {
				free = info["MemFree"]
			}
			return map[string]any{"total": float64(int64(total) / 1024), "used": float64(int64(total-free) / 1024)}
		}
	}
	// macOS
	size, ok1 := cmdOut(3*time.Second, "sysctl", "-n", "hw.memsize")
	stat, ok2 := cmdOut(3*time.Second, "vm_stat")
	if !ok1 || !ok2 {
		return nil
	}
	total, err := strconv.ParseFloat(strings.TrimSpace(size), 64)
	i := strings.Index(stat, "page size of")
	if err != nil || i < 0 {
		return nil
	}
	f := strings.Fields(stat[i+len("page size of"):])
	if len(f) == 0 {
		return nil
	}
	page, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil
	}
	pages := map[string]float64{}
	for _, l := range strings.Split(stat, "\n")[1:] {
		if j := strings.Index(l, ":"); j > 0 {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(l[j+1:]), "."), 64); err == nil {
				pages[strings.TrimSpace(l[:j])] = v
			}
		}
	}
	free := (pages["Pages free"] + pages["Pages inactive"] + pages["Pages speculative"]) * page
	used := total - free
	if used < 0 {
		used = 0
	}
	return map[string]any{"total": float64(int64(total) / (1 << 20)), "used": float64(int64(used) / (1 << 20))}
}

func GpusNow() []any {
	resMu.Lock()
	defer resMu.Unlock()
	if time.Since(gpusAt) < 5*time.Second {
		return gpusCache
	}
	cards := []any{}
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		if out, ok := cmdOut(5*time.Second, "nvidia-smi", "--query-gpu=index,name,utilization.gpu,memory.used,memory.total",
			"--format=csv,noheader,nounits"); ok {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				f := strings.Split(line, ",")
				if len(f) != 5 {
					cards = []any{}
					break
				}
				for i := range f {
					f[i] = strings.TrimSpace(f[i])
				}
				index, e1 := strconv.Atoi(f[0])
				used, e2 := strconv.ParseFloat(f[3], 64)
				total, e3 := strconv.ParseFloat(f[4], 64)
				if e1 != nil || e2 != nil || e3 != nil {
					cards = []any{}
					break
				}
				var util any
				if u, err := strconv.ParseFloat(f[2], 64); err == nil {
					util = u
				}
				cards = append(cards, map[string]any{"index": index, "name": f[1], "util": util, "used": used, "total": total})
			}
		}
	}
	gpusAt, gpusCache = time.Now(), cards
	return cards
}

func shortHost() string { return system.ShortHost() }

// heldNow: what errand's claims hold on this machine, in all
func heldNow() map[string]any {
	cpus, ram, gpus, n, exclusive := 0.0, 0.0, 0, 0, false
	for _, c := range queue.Open().Held() {
		n++
		exclusive = exclusive || c.Exclusive
		cpus += c.Needs.CPUs
		ram += c.Needs.RAM
		if len(c.Devices) > 0 {
			gpus += len(c.Devices)
		} else {
			gpus += int(c.Needs.GPUs)
		}
	}
	return map[string]any{"cpus": cpus, "ram": ram, "gpus": gpus, "exclusive": exclusive, "claims": n}
}

func loadNow() any {
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				return v
			}
		}
	}
	if out, ok := cmdOut(3*time.Second, "sysctl", "-n", "vm.loadavg"); ok {
		if f := strings.Fields(strings.Trim(strings.TrimSpace(out), "{}")); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				return v
			}
		}
	}
	return nil
}

func schedulerNow() any {
	if os.Getenv("ERRAND_ALLOCATION") != "" {
		return nil
	}
	for _, v := range []string{"SLURM_JOB_ID", "PBS_JOBID", "OAR_JOB_ID", "LSB_JOBID", "SGE_TASK_ID", "FLUX_JOB_ID"} {
		if os.Getenv(v) != "" {
			return strings.ToLower(strings.SplitN(v, "_", 2)[0])
		}
	}
	return nil
}

func Measure() map[string]any {
	cpus := runtime.NumCPU()
	out := map[string]any{"cpus": cpus, "busy": cpuBusy(), "load": loadNow(), "ram": ramNow(), "gpus": GpusNow(),
		"held": heldNow(), "scheduler": schedulerNow()}
	if out["busy"] == nil && out["load"] != nil {
		b := out["load"].(float64) / float64(cpus)
		if b > 1 {
			b = 1
		}
		out["busy"] = b // where /proc/stat is not: the load
	}
	return out
}
