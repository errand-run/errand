// The venvs of errand's ( ADR 0044 ): a `Uv` layer's environment, in the store as a conda one is. Its identity is the
// project's `uv.lock` when the layer is the project's own environment; otherwise its packages, resolved by uv for every
// platform at once ( `--universal` ) and written down in `errand-envs.lock`. Built with uv, never changed once built.

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/queue"
)

// pythonVersion: the exact version of the interpreter uv gives for what is asked ( "3.12", or nothing: its default )
func (s setting) pythonVersion(asked string) (string, error) {
	args := []string{"python", "find"}
	if asked != "" {
		args = append(args, asked)
	}
	code, found := s.uvRun(nil, args...)
	if code != 0 { // not installed yet: uv installs it
		install := []string{"python", "install"}
		if asked != "" {
			install = append(install, asked)
		}
		var log strings.Builder
		if code, _ := s.uvRun(&log, install...); code != 0 {
			return "", fmt.Errorf("uv has no python %s here, and could not install it: %s", asked, kit.EndOf(log.String(), 400))
		}
		if code, found = s.uvRun(nil, args...); code != 0 {
			return "", fmt.Errorf("uv finds no python %s here", asked)
		}
	}
	lines := strings.Split(strings.TrimSpace(found), "\n")
	python := strings.TrimSpace(lines[len(lines)-1])
	code, v := s.line(words(python, "-c", "import platform; print( platform.python_version() )"), nil)
	if code != 0 || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s does not say its version", python)
	}
	return strings.TrimSpace(v), nil
}

// resolveUv: what a declaration comes to, for every platform
func (s setting) resolveUv(d layer.Declaration) (layer.Resolution, error) {
	r := layer.Resolution{Resolved: nowSaid()[:10] + " on " + queue.Host()}
	v, err := s.pythonVersion(d.Python)
	if err != nil {
		return r, err
	}
	r.Python = v
	if len(d.Pip) > 0 {
		s.say("    resolving on " + queue.Host() + " ( every platform, python " + v + " )")
		scratch := filepath.Join(layer.MambaStore(), "tmp", fmt.Sprintf("uv-%d-%s", os.Getpid(), kit.RandomID()[:8]))
		if err := os.MkdirAll(scratch, 0o755); err != nil {
			return r, err
		}
		defer os.RemoveAll(scratch)
		in, out := filepath.Join(scratch, "in.txt"), filepath.Join(scratch, "out.txt")
		if err := os.WriteFile(in, []byte(strings.Join(d.Pip, "\n")+"\n"), 0o644); err != nil {
			return r, err
		}
		var log strings.Builder
		if code, _ := s.uvRun(&log, "pip", "compile", "--universal", "--python-version", v, "--generate-hashes", "--no-header", "--no-annotate",
			"-o", out, in); code != 0 {
			return r, fmt.Errorf("the packages cannot be resolved here: %s", kit.EndOf(log.String(), 800))
		}
		data, err := os.ReadFile(out)
		if err != nil {
			return r, err
		}
		r.Pip = parseCompiled(string(data))
		for _, p := range r.Pip {
			if len(p.Hashes) == 0 {
				return r, fmt.Errorf("uv resolved %q without the hashes of its files: errand can only lock packages of an index", p.Pin)
			}
		}
	}
	r.Identify()
	return r, nil
}

// buildUv: a venv made in the store, for a resolution or for the project's `uv.lock`, under its lock -> its record
func (s setting) buildUv(r layer.Resolution, name string, sync bool) (storeRecord, error) {
	dirOf := storeDir("uv")
	release, waited, err := s.holdAt(filepath.Join(dirOf, r.ID+".lock"), name, r.ID)
	if err != nil {
		return storeRecord{}, err
	}
	defer release()
	if rec, ok := builtFor("uv", r.ID); ok && (waited || !s.force) {
		return rec, nil
	}
	dir := freeDir("uv", r.ID)
	prefix := filepath.Join(dirOf, dir)
	s.say("    building " + name + " in " + prefix)
	fail := func(what string, log *strings.Builder) (storeRecord, error) {
		_ = os.RemoveAll(prefix)
		return storeRecord{}, fmt.Errorf("%s: %s", what, kit.EndOf(log.String(), 800))
	}
	var log strings.Builder
	if sync { // the project's own environment, as its uv.lock says: the project itself goes into the overlay
		withEnv := s
		withEnv.environ = append(append([]string{}, s.envOrMine()...), "UV_PROJECT_ENVIRONMENT="+prefix)
		args := []string{"sync", "--frozen", "--no-install-project"}
		if r.Python != "" {
			args = append(args, "--python", r.Python)
		}
		if code, _ := withEnv.uvRun(&log, args...); code != 0 {
			return fail("uv could not install what uv.lock says", &log)
		}
		code, frozen := s.uvRun(nil, "pip", "freeze", "--python", layer.VenvPython(prefix))
		if code == 0 { // what is there, for whoever compares it later
			for _, line := range strings.Split(frozen, "\n") {
				if strings.Contains(line, "==") {
					r.Pip = append(r.Pip, layer.PipPin{Pin: strings.TrimSpace(line)})
				}
			}
		}
	} else {
		if code, _ := s.uvRun(&log, "venv", prefix, "--python", r.Python); code != 0 {
			return fail("uv could not make the venv", &log)
		}
		if len(r.Pip) > 0 {
			reqs := filepath.Join(layer.MambaStore(), "tmp", dir+".pip.txt")
			_ = os.MkdirAll(filepath.Dir(reqs), 0o755)
			var lines []string
			for _, p := range r.Pip {
				line := p.Pin
				for _, h := range p.Hashes {
					line += " --hash=" + h
				}
				lines = append(lines, line)
			}
			if err := os.WriteFile(reqs, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return storeRecord{}, err
			}
			defer os.Remove(reqs)
			log.Reset()
			if code, _ := s.uvRun(&log, "pip", "install", "--python", layer.VenvPython(prefix), "--no-deps", "--require-hashes", "-r", reqs); code != 0 {
				return fail("uv could not install the packages", &log)
			}
		}
	}
	rec := storeRecord{Kind: "uv", ID: r.ID, Dir: dir, Made: nowSaid(), By: whoBuilds(), Used: nowSaid(), Res: r}
	if err := kit.WriteRecord(filepath.Join(prefix, builtMark), map[string]any{"id": r.ID, "made": rec.Made}); err != nil {
		return rec, err
	}
	rec.Stamp = prefixStamp(prefix)
	return rec, writeStore(rec)
}

// envOrMine: the surroundings the commands are given
func (s setting) envOrMine() []string {
	if s.environ != nil {
		return s.environ
	}
	return os.Environ()
}

// ensureUv: a `Uv` layer's venv, made sure of -- as ensureMamba does for a conda one
func (s setting) ensureUv(a layer.Artefact) error {
	l := a.Layer
	d, err := layer.DeclarationOf(l, s.root)
	if err != nil {
		return fmt.Errorf("%s: %v", a.Name, err)
	}
	decl := d.Hash()
	locked, sync := layer.UvLocked(l, s.root)
	var lock *layer.Lock
	var res layer.Resolution
	found := false
	if sync { // its identity is the project's uv.lock
		decl, res, found = locked, layer.Resolution{ID: locked, Python: d.Python}, true
	} else {
		if lock, err = layer.ReadLock(s.root); err != nil {
			return err
		}
		if res, found = lock.Choose(decl, layer.Machine{}); !found {
			if ref, err := layer.ReadRef(s.root, l); err == nil && ref.Declaration == decl {
				if rec, ok := readStore("uv", filepath.Base(ref.Prefix)); ok {
					res, found = rec.Res, true
				}
			}
		}
	}
	rec, built := storeRecord{}, false
	if found {
		rec, built = builtFor("uv", res.ID)
	}
	if built && !s.force {
		prefix := filepath.Join(storeDir("uv"), rec.Dir)
		if stamp := prefixStamp(prefix); stamp != rec.Stamp {
			if said, declare := changedByHand(prefix, rec.Res); len(said) > 0 {
				return errors.New(a.Name + " was changed by hand since errand built it ( " + prefix + " ):\n" + strings.Join(said, "\n") +
					"\nto keep it so, in its declaration:\n  " + strings.Join(declare, "\n  ") +
					"\n`errand setup --force` builds it again; --no-setup runs in it as it is")
			}
			rec.Stamp = stamp
		}
	}
	if s.dryRun {
		switch {
		case !found:
			s.say("    would resolve " + a.Name + " on " + queue.Host() + ", and build it")
		case !built || s.force:
			s.say("    would build " + a.Name + map[bool]string{true: " from uv.lock", false: " from the lock ( " + res.ID + " )"}[sync])
		}
		return nil
	}
	if !found {
		if res, err = s.resolveUv(d); err != nil {
			return fmt.Errorf("%s: %v", a.Name, err)
		}
		rec, built = builtFor("uv", res.ID)
	}
	if !built || s.force {
		if built {
			rec.Replaced = true
			_ = writeStore(rec)
		}
		s.say("  building " + a.Name)
		if rec, err = s.buildUv(res, a.Name, sync); err != nil {
			return fmt.Errorf("%s: %v", a.Name, err)
		}
	}
	rec.Used = nowSaid()
	_ = writeStore(rec)
	prefix := filepath.Join(storeDir("uv"), rec.Dir)
	ref := layer.MambaRef{Root: filepath.Clean(s.root), Name: a.Name, Declaration: decl, ID: res.ID, Prefix: prefix, Seen: nowSaid(),
		UvLock: sync, Python: l.String("python")}
	if old, err := layer.ReadRef(s.root, l); err == nil {
		ref.Overlay, ref.Develop = old.Overlay, old.Develop
	}
	if sync && projectIsAPackage(s.root) { // the project itself, `-e`, over what its lock installs
		d.Develop = append([]layer.Develop{{Path: "."}}, d.Develop...)
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
	if !sync && !lock.Has(decl, res.ID) && s.tell != nil {
		s.tell(map[string]any{"lock": map[string]any{"declaration": decl, "name": a.Name, "resolution": res}})
	}
	return nil
}

// projectIsAPackage: whether the project's pyproject.toml builds a package ( it has a build system )
func projectIsAPackage(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	return err == nil && strings.Contains(string(data), "[build-system]") && strings.Contains(string(data), "[project]")
}
