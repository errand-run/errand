package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Where a view opens is said by errand, here, at the run's end -- and by nothing in the run's own output
func TestWhereAViewOpens(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'box' ] = errand.Env( [ errand.Ssh( host = 'box', root = 'copy/proj' ) ] )\n")
	p.write("job.py", "import errand\nfor i in range( 2 ): errand.view( 'report.html', title = 'Report', content = '<p>' )\nprint( 'done' )\n")
	p.write("job.sh", "#!/bin/sh\neval \"$ERRAND_STEPS_SH\"\necho x > \"$ERRAND_OUT_DIR/shell.html\"; view shell.html; echo done\n")
	check := func(what string, o outcome, name string) {
		t.Helper()
		at := strings.Index(o.err, "↗ "+name+"  file://"+strings.ReplaceAll(p.root, " ", "%20")+"/errand-outputs/")
		if o.code != 0 || at < 0 || strings.Contains(o.out+o.err, "OUTPUT:") || strings.Contains(o.err, p.far) {
			t.Errorf("%s: exit %d\n%s\n%s", what, o.code, o.out, o.err)
		}
		if o.out != "done\n" {
			t.Errorf("%s: its own output: %q", what, o.out)
		}
	}
	check("here", p.errand("", "run", "-o", "job.py"), "Report")
	check("there", p.errand("", "run", "-o", "--env", "box", "job.py"), "Report")
	check("a shell script", p.errand("", "run", "-o", "--env", "box", "./job.sh"), "shell.html")
	o := p.errand("", "run", "-o", "--batch", "-q", "--env", "box", "job.py")
	check("--batch, then attach", p.errand("", "attach", strings.Fields(o.out)[0]), "Report")
	if o = p.errand("", "run", "-q", "-o", "job.py"); strings.Contains(o.err, "Report") {
		t.Errorf("-q: %q", o.err)
	}
	logs, _ := filepath.Glob(filepath.Join(p.root, "errand-outputs", "runs", "*", "*", "output.txt"))
	if len(logs) == 0 {
		t.Error("no output kept")
	}
	for _, f := range logs {
		if strings.Contains(read(t, f), "file://") {
			t.Errorf("%s says a path of where it ran", f)
		}
	}
}
