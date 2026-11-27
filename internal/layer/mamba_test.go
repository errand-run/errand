package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/errand-run/errand/internal/project"
)

// What an environment of errand's is asked to hold: the packages one works on bring their needs, never themselves
func TestADeclaration(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		if err := os.WriteFile(filepath.Join(root, rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("requirements.txt", "# pinned\nrich>=13  # pretty\n\ntomli\n")
	write("sdot/pyproject.toml", `[build-system]
requires = ["meson-python", 'numpy>=2'] # built at import

[project]
name = "SDot_Lib"
dependencies = [
    "numpy>=2",   # the arrays
    "loom",
    "scipy ; python_version >= '3.11'",
]

[project.optional-dependencies]
viz = ["matplotlib"]
`)
	write("loom/pyproject.toml", "[project]\nname = \"loom\"\ndependencies = [ \"jinja2\" ]\n")
	l := project.Layer{"type": "Micromamba", "python": "3.12", "packages": []any{"numpy", "mpich"}, "requirements": "requirements.txt",
		"pip": []any{"tqdm"}, "develop": []any{"sdot[viz]", "loom"}}
	if !MambaOwned(l) || MambaOwned(project.Layer{"type": "Micromamba", "name": "mine"}) {
		t.Fatal("what declares its contents is errand's; what only names an environment is not")
	}
	d, err := DeclarationOf(l, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.Conda, " "); got != "mpich numpy pip python=3.12" {
		t.Errorf("conda: %s", got)
	}
	if got := strings.Join(d.Pip, " | "); got != "jinja2 | matplotlib | meson-python | numpy>=2 | rich>=13 | scipy ; python_version >= '3.11' | tomli | tqdm" {
		t.Errorf("pip: %s", got)
	}
	if d.Develop[0].Name != "sdot-lib" || d.Develop[0].Path != "sdot" || d.Develop[1].Name != "loom" {
		t.Errorf("develop: %+v", d.Develop)
	}
	// what a package one works on is made of is not what the environment is
	before := d.Hash()
	write("sdot/src.py", "x = 1\n")
	if again, _ := DeclarationOf(l, root); again.Hash() != before {
		t.Error("its code changed the environment")
	}
	write("loom/pyproject.toml", "[project]\nname = \"loom\"\ndependencies = [ \"jinja2\", \"ninja\" ]\n")
	if again, _ := DeclarationOf(l, root); again.Hash() == before {
		t.Error("what it needs did not")
	}
	write("loom/pyproject.toml", "[project]\nname = \"loom\"\ndynamic = [ \"dependencies\" ]\n")
	if _, err := DeclarationOf(l, root); err == nil || !strings.Contains(err.Error(), "dynamic") {
		t.Errorf("dynamic dependencies: %v", err)
	}
	write("requirements.txt", "-e ../x\n")
	if _, err := DeclarationOf(project.Layer{"type": "Micromamba", "requirements": "requirements.txt"}, root); err == nil ||
		!strings.Contains(err.Error(), `develop = [ "../x" ]`) {
		t.Errorf("an editable requirement goes in develop: %v", err)
	}
	// what the pip part cannot lock is said with what to write instead, never left to uv's `--require-hashes`
	for _, c := range []struct {
		pip  []any
		says string
	}{
		{[]any{"numpy", "-e", "/data/proj"}, `develop = [ "/data/proj" ]`},
		{[]any{"-e ../sdot"}, `develop = [ "../sdot" ]`},
		{[]any{"--editable=../sdot"}, `develop = [ "../sdot" ]`},
		{[]any{"./loom"}, `develop = [ "./loom" ]`},
		{[]any{"loom @ file:///home/me/loom"}, "develop"},
		{[]any{"--extra-index-url", "https://download.pytorch.org/whl/cu126", "torch"}, "an option of pip"},
	} {
		_, err := DeclarationOf(project.Layer{"type": "Micromamba", "pip": c.pip}, root)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("pip %v: %v", c.pip, err)
		}
	}
	for _, ok := range []string{"torch==2.14.1", "scipy ; python_version >= '3.11'", "pkg @ https://example.org/pkg-1.0.tar.gz"} {
		if err := notARequirement("`pip`", ok, ""); err != nil {
			t.Errorf("%s is a package: %v", ok, err)
		}
	}
}

// Whether a resolution made on one machine is installed as it is on another
func TestAResolutionSuits(t *testing.T) {
	lmo := Machine{Platform: "linux-64", Virtual: map[string][2]string{"__unix": {"0", "0"}, "__linux": {"6.8.0", "0"},
		"__glibc": {"2.39", "0"}, "__cuda": {"12.4", "0"}, "__archspec": {"1", "x86_64_v3"}}}
	jz := Machine{Platform: "linux-64", Virtual: map[string][2]string{"__unix": {"0", "0"}, "__linux": {"4.18.0", "0"},
		"__glibc": {"2.28", "0"}, "__cuda": {"12.2", "0"}, "__archspec": {"1", "x86_64_v4"}}}
	r := Resolution{Platform: "linux-64", Cuda: true, Requires: []string{"__glibc >=2.17,<3.0.a0", "__cuda >=12", "__unix"},
		Constrains: []string{"__cuda >=11.8"}}
	if !r.Suits(lmo) || !r.Suits(jz) {
		t.Errorf("one resolution for both: %v %v", r.Unmet(lmo), r.Unmet(jz))
	}
	r.Requires = append(r.Requires, "__cuda >=12.4")
	if !r.Suits(lmo) || r.Suits(jz) {
		t.Error("a card that is too old")
	}
	if got := r.Unmet(jz); len(got) != 1 || got[0] != "__cuda >=12.4" {
		t.Errorf("said: %v", got)
	}
	if r.Suits(jz.WithoutCuda()) {
		t.Error("a resolution with a card, taken without one")
	}
	cpu := Resolution{Platform: "linux-64", Requires: []string{"__glibc >=2.28"}, Constrains: []string{"__cuda >=12"}}
	if cpu.Suits(lmo) || !cpu.Suits(lmo.WithoutCuda()) {
		t.Error("a resolution for the CPU is not taken where the solver would see a card")
	}
	for _, c := range []struct {
		v, spec string
		ok      bool
	}{{"2.39", ">=2.17,<3.0.a0", true}, {"3.0", ">=2.17,<3.0.a0", false}, {"12.4", "12.*", true}, {"11.8", "12.*|11.8", true},
		{"27.2.1", ">=11.0", true}, {"10.15", ">=11.0", false}, {"1", "1", true}, {"2.28", "~=2.28", false}} {
		if got := versionMatches(c.v, c.spec); got != c.ok {
			t.Errorf("%s %s: %v", c.v, c.spec, got)
		}
	}
}

// The project's lock: resolutions added, what is no longer declared taken out, the file whole
func TestTheLock(t *testing.T) {
	root := t.TempDir()
	l, err := ReadLock(root)
	if err != nil || len(l.Envs) != 0 {
		t.Fatal("no lock is an empty one")
	}
	r := Resolution{Platform: "osx-arm64", Conda: []string{"https://x/python-3.12.conda#sha256:ab"}, Pip: []PipPin{{"rich==13.9.4", []string{"sha256:1"}}}}
	r.Identify()
	if !l.Add("d1", "micromamba:demo", r) || l.Add("d1", "micromamba:demo", r) {
		t.Error("added once")
	}
	l.Add("d0", "micromamba:old", r)
	if !l.Keep(map[string]bool{"d1": true}) || len(l.Envs) != 1 {
		t.Error("what is no longer declared stays")
	}
	if err := l.Write(root); err != nil {
		t.Fatal(err)
	}
	again, err := ReadLock(root)
	if err != nil || !again.Has("d1", r.ID) {
		t.Fatalf("read back: %v", err)
	}
	if got, ok := again.Choose("d1", Machine{Platform: "osx-arm64"}); !ok || got.ID != r.ID {
		t.Error("not chosen where it was made")
	}
	if _, ok := again.Choose("d1", Machine{Platform: "linux-64"}); ok {
		t.Error("chosen on another platform")
	}
	r2 := r
	r2.Platform = "linux-64"
	r2.Identify()
	if r2.ID == r.ID {
		t.Error("two machines, one id")
	}
}
