package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a package declares for a project: read again when a file it reads changes, and in a project with no file of errand's
func TestProviders(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	site := filepath.Join(filepath.Dir(p.root), "site")
	_ = os.MkdirAll(site, 0o755)
	if err := os.WriteFile(filepath.Join(site, "mine.py"), []byte(`import errand
def provide( root ):
    said = root / "places.txt"
    if said.is_file():
        errand.envs[ said.read_text().strip() ] = errand.Env( [ errand.Vars( { "PLACE": said.read_text().strip() } ) ] )
        return [ said ]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	p.env = append(p.env, "ERRAND_PROVIDERS=mine:provide", "PYTHONPATH="+site)
	p.write("places.txt", "one\n")
	p.write("../errand-envs.py", "import errand\nerrand.envs[ 'above' ] = errand.Env( [ ] )\n")
	if o := p.errand("", "--root", p.root, "run", "--env", "one", "sh", "-c", "echo at $PLACE"); o.out != "at one\n" {
		t.Errorf("in a project with no file of errand's: %d %q %q", o.code, o.out, o.err)
	}
	p.write("places.txt", "two\n")
	if o := p.errand("", "--root", p.root, "run", "--env", "two", "sh", "-c", "echo at $PLACE"); o.out != "at two\n" {
		t.Errorf("what it read changed: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("", "--root", p.root, "envs"); !strings.Contains(o.out, "two") {
		t.Errorf("errand envs: %q %q", o.out, o.err)
	}
}
