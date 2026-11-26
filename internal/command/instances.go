// `errand instances`: the errands of this account side by side ( ERRAND_INSTANCE ), and one of them removed.

package command

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
	"github.com/errand-run/errand/internal/units"
)

const instancesUsage = "errand instances | errand instances remove NAME [--yes]"

// InstancesMain: what is said or asked of the instances here
func InstancesMain(args []string) int {
	var words []string
	yes, here := false, false
	for _, a := range args {
		switch a {
		case "--yes":
			yes = true
		case "--here": // ( this machine alone: what another machine's remove asks over here )
			here = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, instancesUsage)
				return 2
			}
			words = append(words, a)
		}
	}
	switch {
	case len(words) == 0:
		return listInstances()
	case len(words) == 2 && words[0] == "remove":
		return removeInstance(words[1], yes, here)
	}
	fmt.Fprintln(os.Stderr, instancesUsage)
	return 2
}

// instancesHere: name -> directory
func instancesHere() map[string]string {
	base := system.Base()
	out := map[string]string{}
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(base, e.Name(), ".errand-instance")); e.IsDir() && err == nil {
			out[e.Name()] = filepath.Join(base, e.Name())
		}
	}
	if _, ok := out["main"]; !ok { // ( the errand in use, laid out as before instances, until it moves )
		out["main"] = system.Cache()
		if system.Instance() != "main" {
			out["main"] = base
		}
	}
	return out
}

func listInstances() int {
	all := instancesHere()
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		state := "nothing runs"
		if system.Running(all[n]) {
			state = "runs"
		}
		mark := "  "
		if n == system.Instance() {
			mark = "* "
		}
		fmt.Printf("%s%-12s %8s  %-13s %s\n", mark, n, sizeText(kit.TreeSize(all[n])), state, all[n])
	}
	return 0
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
}

// removeInstance: its agents and page stopped, its units gone, here and where it went; then its directories, once said
func removeInstance(name string, yes, here bool) int {
	if name == "main" {
		fmt.Fprintln(os.Stderr, "errand instances: main is the errand in use: it is not removed")
		return 2
	}
	dir := filepath.Join(system.Base(), name)
	if _, err := os.Stat(filepath.Join(dir, ".errand-instance")); err != nil {
		if here {
			fmt.Printf("nothing of %s here\n", name)
			return 0
		}
		fmt.Fprintf(os.Stderr, "errand instances: no instance %s here ( %s )\n", name, dir)
		return 1
	}
	_ = os.Setenv("ERRAND_INSTANCE", name) // ( from here on, this program is of that instance: its machines, its configuration, its units )
	config := filepath.Dir(reachConfig())
	var machines []reach.Machine
	if !here {
		for _, m := range reach.Ledger(reach.HereName()) {
			if !m.Here() && m.Spec() != nil {
				machines = append(machines, m)
			}
		}
	}
	fmt.Printf("errand instances remove %s:\n  %s  %s\n", name, dir, sizeText(kit.TreeSize(dir)))
	if _, err := os.Stat(config); err == nil {
		fmt.Printf("  %s\n", config)
	}
	fmt.Printf("  its agents and page stopped, its units given back ( %s* )\n", units.Label(""))
	for _, m := range machines {
		fmt.Printf("  the same on %s\n", m.Name)
	}
	if !yes {
		fmt.Print("remove all of it? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" && a != "o" && a != "oui" {
			fmt.Println("nothing removed")
			return 1
		}
	}
	for _, m := range machines {
		said, err := m.Spec().RemoveInstance(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v %s\n", m.Name, err, said)
			continue
		}
		fmt.Printf("  %s: %s\n", m.Name, kit.FirstLine(said))
	}
	system.StopIn(dir)
	units.UnkeepAll(units.Label(""))
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "errand instances: %v\n", err)
		return 1
	}
	_ = os.RemoveAll(config)
	_ = os.RemoveAll(kit.ShortDir())
	fmt.Printf("%s removed\n", name)
	return 0
}

// reachConfig: a file of this instance's configuration, for where it is
func reachConfig() string {
	return filepath.Join(system.ConfigDir(os.Environ(), kit.EnvOf), "config.json")
}
