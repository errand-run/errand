// The environments of errand's ( ADR 0044 ): resolved on the machine that builds them, written down in the project's lock,
// built in the store of the machine and never changed once built. A changed declaration is another environment; one
// changed by hand stops the run, and says what to declare.
//
// What a machine resolves is said to whoever drives the command ( a note, `lock` ), who writes it into the project's
// lock: the side that drives decides ( ADR 0005 ), and the next push carries it. Until then the store keeps it, and it
// is said again by every command that finds the project's lock without it.

package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/system"
)

// storeRecord: what the store knows of a prefix
type storeRecord struct {
	Kind     string           `json:"kind"` // mamba, uv
	ID       string           `json:"id"`
	Dir      string           `json:"dir"`
	Made     string           `json:"made"`
	By       string           `json:"by"`
	Used     string           `json:"used"`
	Stamp    string           `json:"stamp"`
	Replaced bool             `json:"replaced,omitempty"` // built again beside it: it goes with the tidying
	Res      layer.Resolution `json:"resolution"`
}

const builtMark = "errand-built.json" // in a prefix: it is complete

// storeDir: where the store keeps the environments of a kind ( `mamba`, `uv` )
func storeDir(kind string) string {
	if kind == "" {
		kind = "mamba"
	}
	return filepath.Join(layer.MambaStore(), kind)
}

func mambaDir() string  { return storeDir("mamba") }
func mambaRoot() string { return filepath.Join(layer.MambaStore(), "root") } // its `pkgs/` is micromamba's cache

func complete(prefix string) bool { return kit.Exists(filepath.Join(prefix, builtMark)) }

// ── errand's micromamba ──────────────────────────────────────────────────────

var mambaPlatforms = map[string]string{"darwin/arm64": "osx-arm64", "darwin/amd64": "osx-64", "linux/amd64": "linux-64",
	"linux/arm64": "linux-aarch64", "linux/ppc64le": "linux-ppc64le"}

// micromamba: errand's micromamba, fetched when it is not there yet -- or the one the layer names, which must be
func (s setting) micromamba(exe string, declared bool) error {
	if st, err := os.Stat(exe); err == nil && !st.IsDir() {
		return nil
	}
	if declared {
		return fmt.Errorf("the micromamba named by `executable`, %s, is not there", exe)
	}
	p, ok := mambaPlatforms[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return fmt.Errorf("micromamba has no build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	url := "https://github.com/mamba-org/micromamba-releases/releases/download/" + layer.MambaVersion + "/micromamba-" + p
	s.say("    fetching micromamba " + layer.MambaVersion + " into " + filepath.Dir(exe))
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		return err
	}
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("micromamba could not be fetched: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("micromamba could not be fetched: %s answered %s", url, resp.Status)
	}
	tmp := fmt.Sprintf("%s.%d.part", exe, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("micromamba could not be fetched: %v", err)
	}
	f.Close()
	return os.Rename(tmp, exe)
}

// mambaLine: a micromamba command as a line of shell, with errand's root prefix and no rc file of the user's
func mambaLine(exe string, args ...string) string {
	words := append([]string{exe}, args...)
	for i, w := range words {
		words[i] = layer.Quote(w)
	}
	return strings.Join(words, " ")
}

// machine: what the solver sees of this machine
func (s setting) machine(exe string) (layer.Machine, error) {
	code, said := s.line(mambaLine(exe, "info", "--json", "--no-rc", "-r", mambaRoot())+" 2>/dev/null", nil)
	var info map[string]any
	if code != 0 || json.Unmarshal([]byte(said), &info) != nil {
		return layer.Machine{}, fmt.Errorf("micromamba could not say what this machine is ( `micromamba info --json`, exit %d )", code)
	}
	return layer.MachineFrom(info), nil
}

// ── the store ────────────────────────────────────────────────────────────────

func readStore(kind, dir string) (storeRecord, bool) {
	var r storeRecord
	data, err := os.ReadFile(filepath.Join(storeDir(kind), dir+".json"))
	if err != nil || json.Unmarshal(data, &r) != nil {
		return r, false
	}
	return r, true
}

func writeStore(r storeRecord) error {
	_ = os.MkdirAll(storeDir(r.Kind), 0o755)
	return kit.WriteRecord(filepath.Join(storeDir(r.Kind), r.Dir+".json"), r)
}

// builtFor: the prefix in use for a resolution, if one is complete
func builtFor(kind, id string) (storeRecord, bool) {
	var best storeRecord
	found := false
	for n := 1; n < 100; n++ {
		dir := id
		if n > 1 {
			dir = fmt.Sprintf("%s-%d", id, n)
		}
		r, ok := readStore(kind, dir)
		if !ok {
			if !kit.Exists(filepath.Join(storeDir(kind), dir)) {
				break
			}
			continue
		}
		if !r.Replaced && complete(filepath.Join(storeDir(kind), dir)) {
			best, found = r, true
		}
	}
	return best, found
}

// freeDir: a directory for a resolution that nothing holds yet
func freeDir(kind, id string) string {
	for n := 1; ; n++ {
		dir := id
		if n > 1 {
			dir = fmt.Sprintf("%s-%d", id, n)
		}
		if !kit.Exists(filepath.Join(storeDir(kind), dir)) && !kit.Exists(filepath.Join(storeDir(kind), dir+".json")) {
			return dir
		}
	}
}

// prefixStamp: what a prefix looks like from outside -- rewritten by any install into it, whoever made it
func prefixStamp(prefix string) string {
	var parts []string
	if st, err := os.Stat(filepath.Join(prefix, "conda-meta", "history")); err == nil {
		parts = append(parts, fmt.Sprintf("%d %d", st.ModTime().Unix(), st.Size()))
	}
	for _, site := range sitePackages(prefix) {
		if st, err := os.Stat(site); err == nil {
			parts = append(parts, fmt.Sprintf("%d %d", st.ModTime().Unix(), system.Links(st)))
		}
	}
	return strings.Join(parts, " ")
}

func sitePackages(prefix string) []string {
	got, _ := filepath.Glob(filepath.Join(prefix, "lib", "python3*", "site-packages"))
	if win := filepath.Join(prefix, "Lib", "site-packages"); kit.IsDir(win) && len(got) == 0 {
		got = []string{win}
	}
	sort.Strings(got)
	return got
}

// ── what changed by hand ─────────────────────────────────────────────────────

type installed struct{ name, version, by string }

// condaInstalled: the packages conda put in a prefix, by url
func condaInstalled(prefix string) map[string]installed {
	out := map[string]installed{}
	files, _ := filepath.Glob(filepath.Join(prefix, "conda-meta", "*.json"))
	for _, f := range files {
		var m map[string]any
		if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &m) == nil {
			name, _ := m["name"].(string)
			if name == "" {
				continue
			}
			url, _ := m["url"].(string)
			out[url] = installed{name, fmt.Sprint(m["version"]), "conda"}
		}
	}
	return out
}

// pipInstalled: the distributions of a prefix that conda did not put there, by `name==version`
func pipInstalled(prefix string) map[string]installed {
	out := map[string]installed{}
	for _, site := range sitePackages(prefix) {
		infos, _ := filepath.Glob(filepath.Join(site, "*.dist-info"))
		for _, info := range infos {
			by, _ := os.ReadFile(filepath.Join(info, "INSTALLER"))
			if strings.TrimSpace(string(by)) == "conda" {
				continue
			}
			name, version := metadataOf(filepath.Join(info, "METADATA"))
			if name != "" {
				out[strings.ToLower(name)+"=="+version] = installed{name, version, "pip"}
			}
		}
	}
	return out
}

func metadataOf(path string) (name, version string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Name: "); ok {
			name = v
		} else if v, ok := strings.CutPrefix(line, "Version: "); ok {
			version = v
		}
	}
	return
}

// changedByHand: what is in a prefix and not in its resolution, and the reverse -> lines to say, and what to declare
func changedByHand(prefix string, r layer.Resolution) (said []string, declare []string) {
	conda := condaInstalled(prefix)
	want := map[string]bool{}
	for _, c := range r.Conda {
		url := strings.SplitN(c, "#", 2)[0]
		want[url] = true
		if _, ok := conda[url]; !ok {
			name := filepath.Base(url)
			said = append(said, "  - "+name+" ( conda, taken out )")
			declare = append(declare, "take "+condaName(name)+" out of `packages`")
		}
	}
	for url, p := range conda {
		if !want[url] {
			said = append(said, fmt.Sprintf("  + %s %s ( conda )", p.name, p.version))
			declare = append(declare, fmt.Sprintf("add \"%s=%s\" to `packages`", p.name, p.version))
		}
	}
	pip := pipInstalled(prefix)
	pins := map[string]bool{}
	for _, p := range r.Pip {
		pin := strings.ToLower(strings.TrimSpace(strings.SplitN(p.Pin, ";", 2)[0]))
		pins[pin] = true
		if _, ok := pip[pin]; !ok {
			said = append(said, "  - "+p.Pin+" ( pip, taken out or changed )")
			declare = append(declare, "take "+strings.SplitN(p.Pin, "==", 2)[0]+" out of `pip`, or say which version")
		}
	}
	for pin, p := range pip {
		if !pins[pin] {
			said = append(said, fmt.Sprintf("  + %s %s ( pip )", p.name, p.version))
			declare = append(declare, fmt.Sprintf("add \"%s==%s\" to `pip`", p.name, p.version))
		}
	}
	sort.Strings(said)
	sort.Strings(declare)
	return
}

// condaName: `numpy-2.1.3-py312h….conda` -> `numpy`
func condaName(file string) string {
	file = strings.TrimSuffix(strings.TrimSuffix(file, ".conda"), ".tar.bz2")
	parts := strings.Split(file, "-")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-2], "-")
	}
	return file
}

// ── resolving ────────────────────────────────────────────────────────────────

// resolve: what a declaration comes to on this machine -- the conda part as micromamba solves it, the pip part as uv
// resolves it over what conda gives
func (s setting) resolve(exe string, d layer.Declaration, m layer.Machine) (layer.Resolution, error) {
	r := layer.Resolution{Platform: m.Platform, Cuda: m.HasCuda(), Resolved: time.Now().Format("2006-01-02") + " on " + queue.Host()}
	scratch := filepath.Join(layer.MambaStore(), "tmp", fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()))
	defer os.RemoveAll(scratch)
	args := []string{"create", "--dry-run", "--json", "--no-rc", "-y", "-r", mambaRoot(), "-p", filepath.Join(scratch, "env"), "--override-channels"}
	for _, c := range d.Channels {
		args = append(args, "-c", c)
	}
	args = append(args, d.Conda...)
	line := mambaLine(exe, args...)
	if d.NoCuda {
		line = "CONDA_OVERRIDE_CUDA= " + line
	}
	s.say("    resolving on " + queue.Host() + " ( " + m.Platform + map[bool]string{true: ", a card", false: ""}[r.Cuda] + " )")
	code, said := s.line(line+" 2>/dev/null", nil)
	var got struct {
		Actions struct {
			Link []struct {
				URL        string   `json:"url"`
				Sha256     string   `json:"sha256"`
				Depends    []string `json:"depends"`
				Constrains []string `json:"constrains"`
			} `json:"LINK"`
		} `json:"actions"`
		Success *bool `json:"success"`
	}
	if code != 0 || json.Unmarshal([]byte(said), &got) != nil || len(got.Actions.Link) == 0 {
		return r, fmt.Errorf("the conda packages cannot be resolved here ( exit %d ): %s", code, kit.EndOf(said, 600))
	}
	needs, agrees := map[string]bool{}, map[string]bool{}
	for _, p := range got.Actions.Link {
		if p.Sha256 == "" { // checked by its sha256 where it is installed, whatever way it came ( ADR 0043 ): never an md5
			return r, fmt.Errorf("micromamba gave no sha256 for %s: errand locks nothing it cannot check", p.URL)
		}
		r.Conda = append(r.Conda, p.URL+"#sha256:"+p.Sha256)
		for _, dep := range p.Depends {
			if strings.HasPrefix(dep, "__") {
				needs[dep] = true
			}
		}
		for _, c := range p.Constrains {
			if strings.HasPrefix(c, "__") {
				agrees[c] = true
			}
		}
	}
	r.Requires, r.Constrains = keys(needs), keys(agrees)
	if len(d.Pip) > 0 {
		pins, err := s.resolvePip(exe, r, d, scratch)
		if err != nil {
			return r, err
		}
		r.Pip = pins
	}
	r.Identify()
	return r, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolvePip: the pip part, resolved by uv against the conda part, in a prefix made for that and thrown away -- what
// conda already gives is a constraint, and not pinned again
func (s setting) resolvePip(exe string, r layer.Resolution, d layer.Declaration, scratch string) ([]layer.PipPin, error) {
	env := filepath.Join(scratch, "env")
	if err := s.createFrom(exe, env, r.Conda, scratch, nil); err != nil {
		return nil, err
	}
	python := filepath.Join(env, "bin", "python")
	code, frozen := s.line(uvLine("pip", "freeze", "--python", python), nil)
	if code != 0 {
		return nil, fmt.Errorf("uv could not list what conda installed: %s", frozen)
	}
	given := map[string]bool{}
	var constraints []string
	for _, l := range strings.Split(frozen, "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(l), "=="); ok {
			given[strings.ToLower(normal(name))] = true
			constraints = append(constraints, strings.TrimSpace(l))
		}
	}
	in, cons, out := filepath.Join(scratch, "in.txt"), filepath.Join(scratch, "constraints.txt"), filepath.Join(scratch, "out.txt")
	if err := os.WriteFile(in, []byte(strings.Join(d.Pip, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cons, []byte(strings.Join(constraints, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	var log strings.Builder
	if code, _ := s.line(uvLine("pip", "compile", "--python", python, "--generate-hashes", "--no-header", "--no-annotate",
		"-c", cons, "-o", out, in), &log); code != 0 {
		return nil, fmt.Errorf("the pip packages cannot be resolved here: %s", kit.EndOf(log.String(), 800))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	var pins []layer.PipPin
	for _, pin := range parseCompiled(string(data)) {
		if !given[strings.ToLower(normal(requirementName(pin.Pin)))] {
			pins = append(pins, pin)
		}
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Pin < pins[j].Pin })
	return pins, nil
}

// parseCompiled: what `uv pip compile --generate-hashes` wrote -- each requirement as it is said ( its marker with it ),
// and the hashes of its files
func parseCompiled(text string) []layer.PipPin {
	var pins []layer.PipPin
	for _, l := range strings.Split(strings.ReplaceAll(text, "\\\n", " "), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.Split(l, " --hash=")
		pin := layer.PipPin{Pin: strings.TrimSpace(parts[0])}
		for _, h := range parts[1:] {
			pin.Hashes = append(pin.Hashes, strings.Fields(h)[0])
		}
		pins = append(pins, pin)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Pin < pins[j].Pin })
	return pins
}

var requirementHead = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

func requirementName(r string) string {
	if m := requirementHead.FindStringSubmatch(r); m != nil {
		return m[1]
	}
	return r
}

func normal(n string) string {
	return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(n))
}

// uvLine: a line that finds or fetches uv, then runs it
func uvLine(args ...string) string {
	words := make([]string, len(args))
	for i, a := range args {
		words[i] = layer.Quote(a)
	}
	return layer.UvFind + ` && "$uv" ` + strings.Join(words, " ")
}

// createFrom: a prefix made from an explicit list of conda packages, and the pip part over it
func (s setting) createFrom(exe, prefix string, conda []string, beside string, pip []layer.PipPin) error {
	if err := os.MkdirAll(beside, 0o755); err != nil {
		return err
	}
	explicit := filepath.Join(beside, filepath.Base(prefix)+".explicit.txt")
	if err := os.WriteFile(explicit, []byte("@EXPLICIT\n"+strings.Join(conda, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	var log strings.Builder
	if code, _ := s.line(mambaLine(exe, "create", "-y", "--no-rc", "-r", mambaRoot(), "-p", prefix, "--file", explicit), &log); code != 0 {
		return fmt.Errorf("micromamba could not make %s ( exit %d ): %s", prefix, code, kit.EndOf(log.String(), 800))
	}
	if len(pip) == 0 {
		return nil
	}
	var lines []string
	for _, p := range pip {
		line := p.Pin
		for _, h := range p.Hashes {
			line += " --hash=" + h
		}
		lines = append(lines, line)
	}
	reqs := filepath.Join(beside, filepath.Base(prefix)+".pip.txt")
	if err := os.WriteFile(reqs, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	log.Reset()
	if code, _ := s.line(uvLine("pip", "install", "--python", filepath.Join(prefix, "bin", "python"), "--no-deps", "--require-hashes", "-r", reqs), &log); code != 0 {
		return fmt.Errorf("uv could not install the pip part into %s ( exit %d ): %s", prefix, code, kit.EndOf(log.String(), 800))
	}
	return nil
}

// buildResolution: a resolution built in the store, under its lock -> the record of the prefix
func (s setting) buildResolution(exe string, r layer.Resolution, name string) (storeRecord, error) {
	lock := filepath.Join(mambaDir(), r.ID+".lock")
	release, waited, err := s.holdAt(lock, name, r.ID)
	if err != nil {
		return storeRecord{}, err
	}
	defer release()
	if rec, ok := builtFor("mamba", r.ID); ok && (waited || !s.force) {
		return rec, nil // somebody built it meanwhile
	}
	dir := freeDir("mamba", r.ID)
	prefix := filepath.Join(mambaDir(), dir)
	s.say("    building " + name + " in " + prefix)
	if err := s.createFrom(exe, prefix, r.Conda, filepath.Join(layer.MambaStore(), "tmp"), r.Pip); err != nil {
		_ = os.RemoveAll(prefix) // half made: nothing uses it
		return storeRecord{}, err
	}
	rec := storeRecord{Kind: "mamba", ID: r.ID, Dir: dir, Made: nowSaid(), By: whoBuilds(), Used: nowSaid(), Stamp: prefixStamp(prefix), Res: r}
	if err := kit.WriteRecord(filepath.Join(prefix, builtMark), map[string]any{"id": r.ID, "made": rec.Made}); err != nil {
		return rec, err
	}
	rec.Stamp = prefixStamp(prefix)
	return rec, writeStore(rec)
}

// ── an environment of errand's, made sure of ─────────────────────────────────

// ensureMamba: an environment of errand's, resolved if its declaration is not in the lock for a machine like this one,
// built if the store does not have it, refused if it was changed by hand
func (s setting) ensureMamba(a layer.Artefact) error {
	l := a.Layer
	d, err := layer.DeclarationOf(l, s.root)
	if err != nil {
		return fmt.Errorf("%s: %v", a.Name, err)
	}
	decl := d.Hash()
	exe := layer.MambaExe(l)
	if s.dryRun && !kit.Exists(exe) {
		s.say("    would fetch micromamba " + layer.MambaVersion + ", resolve and build " + a.Name)
		return nil
	}
	if err := s.micromamba(exe, l.String("executable") != ""); err != nil {
		return err
	}
	m, err := s.machine(exe)
	if err != nil {
		return err
	}
	if d.NoCuda {
		m = m.WithoutCuda()
	}
	lock, err := layer.ReadLock(s.root)
	if err != nil {
		return err
	}
	res, locked := lock.Choose(decl, m)
	if !locked { // what the store resolved for it already, not in the project's lock yet: said again below
		if ref, err := layer.ReadRef(s.root, l); err == nil && ref.Declaration == decl {
			if rec, ok := readStore("mamba", filepath.Base(ref.Prefix)); ok && rec.Res.Suits(m) {
				res, locked = rec.Res, true
			}
		}
	}
	rec, built := storeRecord{}, false
	if locked {
		rec, built = builtFor("mamba", res.ID)
	}
	if built && !s.force {
		prefix := filepath.Join(mambaDir(), rec.Dir)
		if stamp := prefixStamp(prefix); stamp != rec.Stamp {
			said, declare := changedByHand(prefix, rec.Res)
			if len(said) > 0 {
				msg := a.Name + " was changed by hand since errand built it ( " + prefix + " ):\n" + strings.Join(said, "\n")
				msg += "\nto keep it so, in its declaration:\n  " + strings.Join(declare, "\n  ")
				msg += "\n`errand setup --force` builds it again from the lock; --no-setup runs in it as it is"
				return errors.New(msg)
			}
			rec.Stamp = stamp // touched, nothing changed ( a cache written beside the packages )
		}
	}
	if s.dryRun {
		switch {
		case !locked:
			s.say("    would resolve " + a.Name + " on " + queue.Host() + ", and build it")
		case !built || s.force:
			s.say("    would build " + a.Name + " from the lock ( " + res.ID + " )")
		}
		return nil
	}
	if !locked {
		if res, err = s.resolve(exe, d, m); err != nil {
			return fmt.Errorf("%s: %v", a.Name, err)
		}
		rec, built = builtFor("mamba", res.ID)
	}
	if !built || s.force {
		if built { // built again beside it: the one there goes with the tidying, once nothing runs in it
			rec.Replaced = true
			_ = writeStore(rec)
		}
		s.say("  building " + a.Name)
		if rec, err = s.buildResolution(exe, res, a.Name); err != nil {
			return fmt.Errorf("%s: %v", a.Name, err)
		}
	}
	rec.Used = nowSaid()
	_ = writeStore(rec)
	prefix := filepath.Join(mambaDir(), rec.Dir)
	ref := layer.MambaRef{Root: filepath.Clean(s.root), Name: a.Name, Declaration: decl, ID: res.ID, Prefix: prefix, Exe: exe, Seen: nowSaid()}
	if old, err := layer.ReadRef(s.root, l); err == nil {
		ref.Overlay, ref.Develop = old.Overlay, old.Develop
	}
	if err := s.overlay(a, d, &ref); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(layer.RefPath(s.root, l)), 0o755); err != nil {
		return err
	}
	if err := kit.WriteRecord(layer.RefPath(s.root, l), ref); err != nil {
		return err
	}
	if !lock.Has(decl, res.ID) && s.tell != nil { // for whoever drives, to write into the project's lock
		s.tell(map[string]any{"lock": map[string]any{"declaration": decl, "name": a.Name, "resolution": res}})
	}
	return nil
}

// overlay: the packages one works on, installed `-e` in a venv over the environment, per project ( ADR 0044 )
func (s setting) overlay(a layer.Artefact, d layer.Declaration, ref *layer.MambaRef) error {
	if len(d.Develop) == 0 {
		ref.Overlay, ref.Develop = "", ""
		return nil
	}
	var paths []string
	for _, dev := range d.Develop {
		p := dev.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(s.root, p)
		}
		paths = append(paths, filepath.Clean(p))
	}
	want := ref.Prefix + "\x00" + strings.Join(paths, "\x00")
	where, _ := cachesOf(s.outputs, s.root, s.environ)
	dir := filepath.Join(where, caches.BuildsCache, a.Slug(), "develop")
	python := layer.VenvPython(dir)
	if ref.Develop == want && kit.Exists(python) {
		ref.Overlay = python
		return nil
	}
	s.say("    the packages one works on, over " + a.Name + ": " + strings.Join(l(d.Develop), ", "))
	_ = os.RemoveAll(dir)
	if _, err := caches.MakeBuild(filepath.Join(where, caches.BuildsCache), a.Slug(), "develop"); err != nil {
		return err
	}
	var log strings.Builder
	if code, _ := s.line(words(layer.VenvPython(ref.Prefix), "-m", "venv", "--system-site-packages", dir), &log); code != 0 {
		return fmt.Errorf("the overlay of %s could not be made: %s", a.Name, kit.EndOf(log.String(), 600))
	}
	// what the environment holds, seen from the overlay: a venv sees its base interpreter's packages, which for a venv
	// of uv are not the environment's
	if sites, base := sitePackages(dir), sitePackages(ref.Prefix); len(sites) > 0 && len(base) > 0 {
		if err := os.WriteFile(filepath.Join(sites[0], "errand-environment.pth"), []byte(strings.Join(base, "\n")+"\n"), 0o644); err != nil {
			return err
		}
	}
	args := []string{"pip", "install", "--python", python, "--no-deps"}
	for _, p := range paths {
		args = append(args, "-e", p)
	}
	log.Reset()
	if code, _ := s.uvRun(&log, args...); code != 0 {
		return fmt.Errorf("the packages of `develop` could not be installed over %s: %s", a.Name, kit.EndOf(log.String(), 800))
	}
	ref.Overlay, ref.Develop = python, want
	return nil
}

// words: a command as a line, each word quoted -- which a shell reads, and Windows splits the same way
func words(argv ...string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = layer.Quote(a)
	}
	return strings.Join(out, " ")
}

// uvRun: uv, found or fetched, with these arguments
func (s setting) uvRun(out io.Writer, args ...string) (int, string) {
	if layer.OnWindows { // ( a line of one command: `uv` is found by whoever runs it )
		return s.line(words(append([]string{"uv"}, args...)...), out)
	}
	return s.line(uvLine(args...), out)
}

func l(devs []layer.Develop) []string {
	out := make([]string, len(devs))
	for i, d := range devs {
		out[i] = d.Path
	}
	return out
}

// stateOfMamba: what `errand envs` says of an environment of errand's here
func (s setting) stateOfMamba(a layer.Artefact) string {
	d, err := layer.DeclarationOf(a.Layer, s.root)
	if err != nil {
		return "unknown ( " + kit.FirstLine(err.Error()) + " )"
	}
	ref, err := layer.ReadRef(s.root, a.Layer)
	if err != nil || !complete(ref.Prefix) {
		return "not built"
	}
	want := d.Hash()
	if locked, ok := layer.UvLocked(a.Layer, s.root); ok {
		want = locked
	}
	if ref.Declaration != want {
		return "stale"
	}
	rec, ok := readStore(filepath.Base(filepath.Dir(ref.Prefix)), filepath.Base(ref.Prefix))
	if !ok {
		return "not built"
	}
	if prefixStamp(ref.Prefix) != rec.Stamp {
		if said, _ := changedByHand(ref.Prefix, rec.Res); len(said) > 0 {
			return "changed by hand"
		}
	}
	return "ok"
}

// Python: the interpreter the innermost layer of a stack that names one runs, on this machine ( `errand python` )
func Python(stack []project.Layer, root string) (string, error) {
	for i := len(stack) - 1; i >= 0; i-- {
		l := stack[i]
		switch {
		case layer.IsContainer(l):
			return "", fmt.Errorf("the interpreter is inside %s: `errand shell` enters it", layer.Describe(stack[i:i+1]))
		case layer.StoreOwned(l):
			return layer.Interpreter(root, l)
		case l.Type() == "Micromamba" || l.Type() == "Conda":
			return "", fmt.Errorf("%s is an environment of yours: `micromamba run -n %s which python` says", layer.Describe(stack[i:i+1]), l.String("name"))
		case l.Type() == "Venv":
			p := l.String("python")
			if p == "" {
				p = "python"
			}
			if strings.ContainsRune(p, '/') && !filepath.IsAbs(p) {
				p = filepath.Join(root, p)
			}
			return p, nil
		}
	}
	return "", errors.New("no layer of this environment names an interpreter: the machine's python runs")
}

// notedImage: an image of errand's cache is used by a layer of the project at `root`, now
func notedImage(root string, l project.Layer, image string) {
	_ = os.WriteFile(image+".used", []byte(nowSaid()+"\n"), 0o644)
	_ = os.MkdirAll(filepath.Dir(layer.RefPath(root, l)), 0o755)
	_ = kit.WriteRecord(layer.RefPath(root, l), layer.MambaRef{Root: filepath.Clean(root), Name: "image", ID: filepath.Base(image), Prefix: image,
		Layer: l, Seen: nowSaid()})
}
