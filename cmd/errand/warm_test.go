package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWarmStarts(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ "warm" ] = errand.Env( [ errand.Vars( { "V": "1" } ), errand.Preload( "slowlib" ) ] )
errand.envs[ "not" ] = errand.Env( [ errand.Missing( "a Mac reachable by ssh", hint = "name one in hosts.py" ) ] )
errand.envs[ "nix" ] = errand.Env( [ errand.Nix( shell = "dev" ) ] )
errand.default_env = "warm"
`)
	libs := filepath.Join(filepath.Dir(p.root), "libs")
	imported := filepath.Join(libs, "imported")
	_ = os.MkdirAll(libs, 0o755)
	_ = os.WriteFile(filepath.Join(libs, "slowlib.py"), []byte("import os\nopen( '"+imported+"', 'a' ).write( str( os.getpid() ) + ' ' )\n"), 0o644)
	p.env = append(p.env, "PYTHONPATH="+libs)
	p.write("w.py", "import sys, os, slowlib\nprint( 'ran', os.environ[ 'V' ], sys.argv[ 1 : ], os.getcwd(), __name__, sys.stdin.read() )\nsys.exit( int( sys.argv[ 1 ] ) )\n")
	for i, want := range []string{"0", "3", "0"} {
		o := p.errand("typed", "run", "-q", "w.py", want, "a b")
		if strconv.Itoa(o.code) != want || !strings.Contains(o.out, "ran 1 ['"+want+"', 'a b'] "+p.root+" __main__ typed") {
			t.Fatalf("a warm run ( %d ): exit %d %q %q", i, o.code, o.out, o.err)
		}
		if i == 0 { // ( what is named is imported once nothing is asked of it )
			time.Sleep(2500 * time.Millisecond)
			_ = os.WriteFile(imported, nil, 0o644)
		}
	}
	if got := read(t, imported); got != "" {
		logs, _ := filepath.Glob(filepath.Join(p.cache(), "place", "*", "*", "warm", "*", "log"))
		said := ""
		for _, l := range logs {
			said += read(t, l)
		}
		t.Errorf("a run imported again what was imported ahead: %q ( %q %s )", got, logs, said)
	}
	p.env = append(p.env, "ERRAND_WARM=0")
	if o := p.errand("", "run", "-q", "w.py", "0"); o.code != 0 || read(t, imported) == "" {
		t.Errorf("ERRAND_WARM=0 starts cold: exit %d %q", o.code, o.err)
	}
	if o := p.errand("", "run", "--env", "not", "w.py", "0"); o.code == 0 || !strings.Contains(o.err, "needs a Mac reachable by ssh") || !strings.Contains(o.err, "name one in hosts.py") {
		t.Errorf("an environment that is missing says what: exit %d %q", o.code, o.err)
	}
	tools := p.tools()
	_ = os.WriteFile(filepath.Join(tools, "nix"), []byte("#!/bin/sh\necho \"nix $*\"\n"), 0o755)
	if o := p.errand("", "run", "-q", "--env", "nix", "echo", "x"); o.out != "nix develop .#dev -c echo x\n" {
		t.Errorf("nix: %q %q", o.out, o.err)
	}
}
