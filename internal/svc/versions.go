// The versions of services on this machine, what each costs, whether anything still needs it ( ADR 0028 §2 ).
//
// A version ( `blender#63ecc287` ) brings with it its declaration, its directory, its env, and a share of the code fetched
// for it. It is USED when a store still present among the views registered here asks for it, when it runs, or when it ran
// or was authorized within N days. Unused, it goes with all that is its own; fetched code goes with the last version that
// needed it, or once nobody has needed it for N days. A pinned version stays.

package svc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
)

const VersionDays = 14.0 // unused after this many days

type serviceVersionHere struct {
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Authorized bool     `json:"authorized"`
	Pinned     bool     `json:"pinned"`
	State      string   `json:"state"` // how it runs; authorized; left, what of one that is not
	Last       float64  `json:"last"`
	Stores     []string `json:"stores"`
	Env        string   `json:"env"`
	Dir        string   `json:"keeper"`
	dirs       []string
	Code       []string `json:"code"`
	Size       int64    `json:"size"`
	Whole      bool     `json:"whole"`
	Unused     bool     `json:"unused"`
	runs       bool
}

type CodeHere struct {
	Path   string   `json:"path"`
	Git    string   `json:"git"`
	Rev    string   `json:"rev"`
	Users  []string `json:"users"`
	At     float64  `json:"at"`
	Size   int64    `json:"size"`
	Unused bool     `json:"unused"`
}

func changedAt(paths ...string) float64 {
	last := 0.0
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && p != "" {
			last = max(last, kit.Seconds(st.ModTime()))
		}
	}
	return last
}

// storesAsking: the views registered here whose directory is still there, by the version they ask for
func storesAsking() map[string][]string {
	out := map[string][]string{}
	views := RegisteredViews()
	for _, vid := range sortedKeys(views) {
		dir := views[vid]
		if !kit.IsDir(dir) {
			continue
		}
		for name, want := range AskedServices(dir) {
			if want.Version != "" {
				out[want.Key(name)] = append(out[want.Key(name)], dir)
			}
		}
	}
	return out
}

// VersionsHere: what is here, with its sizes and uses. `running`: the services the agents of this machine hold.
func VersionsHere(running map[string]string, sizes bool, days float64) ([]*serviceVersionHere, []*CodeHere) {
	now := kit.Seconds(time.Now())
	declared, asked, code := PageAuthorized(), storesAsking(), fetchedCode()
	codeDirs := sortedKeys(code)
	keys, envs := map[string]bool{}, map[string]string{}
	for k := range declared {
		keys[k] = strings.Contains(k, "#")
	}
	made, _ := filepath.Glob(filepath.Join(serviceEnvsDir(), "*@*"))
	for _, e := range made {
		if kit.IsDir(e) {
			k := strings.Replace(filepath.Base(e), "@", "#", 1)
			envs[k], keys[k] = e, true
		}
	}
	served := map[string][]string{}
	for _, d := range ServingDirs() {
		if strings.Contains(d.Label, "#") {
			served[d.Label], keys[d.Label] = append(served[d.Label], d.Dir), true
		}
	}
	var versions []*serviceVersionHere
	for _, k := range sortedKeys(keys) {
		if !keys[k] {
			continue
		}
		d := declared[k]
		v := &serviceVersionHere{Key: k, Authorized: d != nil, Pinned: d != nil && d.Pinned, State: "left", Stores: asked[k], Env: envs[k], Code: []string{}, Whole: true}
		v.Name, v.Version = splitKey(k)
		if v.Stores == nil {
			v.Stores = []string{}
		}
		if d != nil {
			v.State = "authorized"
		}
		if st := running[k]; st != "" {
			v.State, v.runs = st, true
		}
		if v.dirs = served[k]; len(v.dirs) > 0 {
			v.Dir = v.dirs[0]
		}
		v.Last = changedAt(filepath.Join(v.Dir, "keeper.log"), v.Env)
		if d != nil && !parseStamp(d.Since).IsZero() {
			at := parseStamp(d.Since)
			v.Last = max(v.Last, kit.Seconds(at))
		}
		for _, c := range codeDirs { // what it runs from: its provider's, or a path of its command
			words := []string{}
			if d != nil {
				words = append(d.Command(), d.Provider)
			}
			for _, w := range words {
				if w == c || strings.HasPrefix(w, c+string(os.PathSeparator)) {
					v.Code = append(v.Code, c)
					break
				}
			}
		}
		if sizes && v.Env != "" {
			v.Size, v.Whole = caches.SizeWithin(v.Env, caches.SizeBudget)
		}
		v.Unused = !v.runs && !v.Pinned && len(v.Stores) == 0 && now-v.Last > days*86400
		versions = append(versions, v)
	}
	var out []*CodeHere
	for _, c := range codeDirs {
		one := &CodeHere{Path: c, Git: kit.Text(code[c]["git"]), Rev: kit.Text(code[c]["rev"]), Users: []string{}, At: changedAt(filepath.Join(c, sourceFile))}
		for _, v := range versions {
			for _, used := range v.Code {
				if used == c {
					one.Users = append(one.Users, v.Key)
				}
			}
		}
		one.Unused = len(one.Users) == 0 && now-one.At > days*86400
		if sizes {
			one.Size, _ = caches.SizeWithin(c, caches.SizeBudget)
		}
		out = append(out, one)
	}
	return versions, out
}

func removeTree(path string) int64 {
	n, _ := caches.SizeWithin(path, 30*time.Second)
	if os.RemoveAll(path) != nil {
		return 0
	}
	return n
}

// removeVersion: a version gone -- its declaration, its directory, its env, the fetched code only it used
func removeVersion(v *serviceVersionHere, all []*serviceVersionHere) (int64, error) {
	if v.runs {
		return 0, fmt.Errorf("%s is %s: errand serve stop %s first", v.Key, v.State, v.Key)
	}
	if v.Authorized {
		if err := dropPage(v.Key); err != nil {
			return 0, err
		}
	}
	var freed int64
	for _, p := range append([]string{v.Env}, v.dirs...) {
		if p != "" {
			freed += removeTree(p)
		}
	}
	for _, c := range v.Code {
		shared := false
		for _, o := range all {
			for _, used := range o.Code {
				shared = shared || o != v && used == c
			}
		}
		if !shared {
			freed += removeTree(c)
		}
	}
	return freed, nil
}

// PruneVersions: the versions unused for `days`, and the fetched code nobody has needed for as long, gone
func PruneVersions(running map[string]string, days float64, dry bool) map[string]any {
	versions, code := VersionsHere(running, dry, days)
	gone, freed := []string{}, int64(0)
	for _, v := range versions {
		if !v.Unused {
			continue
		}
		if dry {
			freed += v.Size
			gone = append(gone, v.Key)
		} else if n, err := removeVersion(v, versions); err == nil {
			freed += n
			gone = append(gone, v.Key)
		}
	}
	if !dry {
		_, code = VersionsHere(running, false, days)
	}
	left := []*CodeHere{}
	for _, c := range code {
		if !c.Unused {
			continue
		}
		left = append(left, c)
		if dry {
			freed += c.Size
		} else {
			freed += removeTree(c.Path)
		}
	}
	return map[string]any{"versions": gone, "code": left, "freed": freed}
}

// PinVersion: a version kept out of the purge, or not any more
func PinVersion(key string, on bool) error { return PinPage(key, on) }
