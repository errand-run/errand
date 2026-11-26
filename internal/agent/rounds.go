// What a machine's agent does by itself, at its start and every hour ( ADR 0022, 0029 ): the library caches it holds,
// each purged at most once a day; what errand left in its own directory and nothing needs any more; the copies whose
// removal a machine did not answer. Nothing of it is a reason for anything to fail.

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
)

const (
	roundEvery  = time.Hour
	programDays = 7 * 24 * time.Hour // a program, a copy of the Python, another machine sent and that nothing used since
	placeDays   = 7 * 24 * time.Hour // the directory of an agent that is gone, and its jobs' records
)

func (a *Place) rounds() {
	if os.Getenv("ERRAND_NO_ROUNDS") == "1" {
		return
	}
	for wait := 30 * time.Second; ; wait = roundEvery {
		time.Sleep(wait)
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintln(os.Stderr, "errand agent: a round:", kit.SaidError(r))
				}
			}()
			say := func(line string) { fmt.Fprintln(os.Stderr, "errand agent:", line) }
			for _, gone := range a.Tidy(time.Now()) {
				say("removed " + gone)
			}
			a.dropCopies(nil, true)
			_ = caches.PurgeHeld(say)
			a.pruneOld(say)
		}()
	}
}

// Tidy: what errand left in its directory here and nothing needs any more, removed -> what went
func (a *Place) Tidy(now time.Time) []string {
	var gone []string
	remove := func(path, why string) {
		if os.RemoveAll(path) == nil {
			gone = append(gone, path+": "+why)
		}
	}
	old := func(path string, than time.Duration) bool {
		st, err := os.Lstat(path)
		return err == nil && now.Sub(st.ModTime()) > than
	}
	mine, current := filepath.Dir(kit.SelfPath()), currentHash()
	for _, kind := range []string{"bin", "binding"} {
		dirs, _ := filepath.Glob(filepath.Join(kit.LocalCache(), kind, "*"))
		for _, d := range dirs {
			if st, err := os.Stat(d); err != nil || !st.IsDir() || d == mine {
				continue
			}
			used := d
			if kind == "bin" {
				used = filepath.Join(d, "errand")
				if filepath.Base(d) == current {
					continue // what the machine's stable name leads to ( current.go )
				}
				if !kit.Exists(used) || reach.AgentAlive(filepath.Join(a.Root, filepath.Base(d))) {
					continue // ( a program that runs is used )
				}
			}
			if old(used, programDays) && old(d, programDays) {
				remove(d, "nothing used it for a week")
			}
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "place", "*", "*"))
	for _, d := range dirs {
		if d == a.Dir || reach.AgentAlive(d) || !old(d, placeDays) {
			continue
		}
		recent := false
		jobs, _ := filepath.Glob(filepath.Join(d, "jobs", "*"))
		for _, j := range jobs {
			if _, ended := jobExit(j); !ended || !old(filepath.Join(j, "exit"), placeDays) {
				recent = true
			}
		}
		if !recent && filepath.Dir(d) == a.Root { // ( another machine's, on a home they share, is that one's to remove )
			remove(d, "an agent that is gone")
		}
	}
	base := kit.ShortDir()
	if st, err := os.Lstat(base); err == nil && st.IsDir() {
		links, _ := os.ReadDir(base)
		for _, l := range links {
			p := filepath.Join(base, l.Name())
			if l.Type()&os.ModeSymlink != 0 {
				if _, err := os.Stat(p); err != nil {
					remove(p, "a link to a directory that is gone")
				}
			}
		}
	}
	return gone
}

// pruneOld: the services' versions nothing needs ( ADR 0028 ), once a day
func (a *Place) pruneOld(say func(string)) {
	if time.Since(a.prunedAt) < 24*time.Hour {
		return
	}
	a.prunedAt = time.Now()
	got := svc.PruneVersions(a.servingHere(), svc.VersionDays, false)
	versions, _ := got["versions"].([]string)
	code, _ := got["code"].([]*svc.CodeHere)
	if len(versions)+len(code) > 0 {
		say(fmt.Sprintf("services: %d unused version(s) and %d fetched code dir(s) removed, %s", len(versions), len(code), caches.Human(float64(got["freed"].(int64)))))
	}
}

// dropCopies: copies removed where they are -- those said, or ( `pending` ) those a machine did not answer about and
// those of what is gone here -> how many went, and what could not be
func (a *Place) dropCopies(those []filesync.CopyNote, pending bool) (int, []string) {
	if pending {
		for _, c := range filesync.CopiesKnown() {
			if _, err := os.Stat(c.Path); c.State == "removing" || os.IsNotExist(err) {
				those = append(those, c)
			}
		}
	}
	by := map[string][]filesync.CopyNote{}
	for _, c := range those {
		by[c.Host.Key()] = append(by[c.Host.Key()], c)
	}
	removed, kept := 0, []string{}
	for _, mine := range by {
		var vids []string
		for _, c := range mine {
			vids = append(vids, c.Vid)
		}
		t := &reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}
		l, err := reach.LinkTo(t, &mine[0].Host)
		if err == nil {
			_, err = l.Ask(map[string]any{"op": "services_do", "req": map[string]any{"do": "copies_remove", "vids": vids}})
		}
		t.Close()
		if err != nil {
			kept = append(kept, mine[0].Machine+": "+kit.FirstLine(err.Error()))
			for _, c := range mine {
				c.State = "removing"
				c.Write()
			}
			continue
		}
		for _, c := range mine {
			_ = os.Remove(c.File())
			removed++
		}
	}
	return removed, kept
}

// serveCopies: the copies a page asks to remove
func (a *Place) serveCopies(req map[string]any, f *reach.FarChan) {
	var those []filesync.CopyNote
	for _, c := range filesync.CopiesKnown() {
		for _, id := range kit.Strs(req["ids"]) {
			if c.ID == id {
				those = append(those, c)
			}
		}
	}
	n, kept := a.dropCopies(those, false)
	if len(kept) > 0 {
		f.Fail("%s ( asked again every hour )", strings.Join(kept, "; "))
		return
	}
	f.Reply(map[string]any{"ok": true, "removed": n})
}

// ── a machine shared with others ( ADR 0028 §7 ) ─────────────────────────────

const (
	sharedAgent   = 120.0         // seconds an agent stays there with nothing asked
	sharedIdle    = 600.0         // a service unused goes after this, whatever it declares
	sharedService = 2 * time.Hour // and in any case after this
)

// sharedHere: whether this machine is one errand leaves nothing on -- said by whoever reaches it, else a cluster's
func sharedHere() bool {
	switch os.Getenv("ERRAND_SHARED") {
	case "0":
		return false
	case "1":
		return true
	}
	if os.Getenv("SLURM_JOB_ID") != "" {
		return true
	}
	for _, tool := range []string{"sbatch", "squeue"} {
		if _, err := kit.LookIn(tool, os.Environ(), "."); err == nil {
			return true
		}
	}
	return false
}
