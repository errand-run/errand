package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

// aWindows: ssh and scp that reach, for a moment, a Windows -- cmd.exe's lines are read by a stand-in that knows those
// errand says, the program that is sent is the one under test, and paths of drives are directories of a home
func aWindows(t *testing.T, p *scene) string {
	t.Helper()
	base := filepath.Dir(p.root)
	guest := filepath.Join(base, "guest")
	_ = os.MkdirAll(filepath.Join(guest, "home"), 0o755)
	_ = os.MkdirAll(filepath.Join(guest, "queue"), 0o755)
	_ = os.WriteFile(filepath.Join(base, "errand-windows-amd64"), []byte("a program for another system"), 0o755)
	ssh := filepath.Join(base, "ssh")
	plain := read(t, ssh)
	_ = os.WriteFile(filepath.Join(base, "ssh-plain"), []byte(plain), 0o755)
	_ = os.WriteFile(ssh, []byte(`#!/bin/sh
all="$*"
while [ $# -gt 0 ]; do case "$1" in -o|-p|-i|-J) shift 2;; -*) shift;; *) break;; esac; done
host="$1"; shift
case "$host" in winbox|*@127.0.0.1) ;; *) exec `+base+`/ssh-plain $host "$@";; esac
printf "%s\n" "$all" >> `+guest+`/log
case "$*" in
"echo errand-ok") [ -f `+guest+`/off ] && exit 255; echo errand-ok;;
*PROCESSOR_ARCHITECTURE*) echo "__errand__=AMD64=C:\\Users\\x=";;
*EncodedCommand*) echo errand-data-ok; echo 'errand-python=C:\Python\python.exe ';;
*setup.status*) [ -f `+guest+`/wrong ] && echo "failed: no way" || echo ok;;
"echo ready"*) touch "`+guest+`/ready";;
*"echo errand-ready"*) [ -f "`+guest+`/ready" ] && echo errand-ready; exit 0;;
*" there") [ -f `+guest+`/shipped ] || { echo "The system cannot find the path specified." >&2; exit 1; }
    export HOME=`+guest+`/home ERRAND_CACHE_DIR=`+guest+`/cache XDG_RUNTIME_DIR=`+guest+`/queue ERRAND_SSH= SHELL=/bin/sh
    cd "$HOME" && exec `+built+` there;;
*move*) touch `+guest+`/shipped;;
*) exit 0;;
esac
`), 0o755)
	scp := filepath.Join(base, "scp")
	_ = os.WriteFile(scp, []byte("#!/bin/sh\necho \"scp $*\" >> "+guest+"/log\n"), 0o755)
	p.env = append(p.env, "ERRAND_SCP="+scp, "ERRAND_DIST="+base)
	return guest
}

func TestAWindowsBox(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	guest := aWindows(t, p)
	p.write("errand-envs.py", `import errand
errand.envs[ "win" ] = errand.Env( [ errand.Ssh( host = "winbox", options = [ "-p", "2222" ] ), errand.WindowsBox( fetch = [ "made" ] ), errand.Vars( { "WHERE": "there" } ) ] )
`)
	p.write("job.sh", "#!/bin/sh\necho in $(pwd) $WHERE; mkdir -p made; echo m > made/m.txt\n")
	o := p.errand("", "run", "-q", "--env", "win", "job.sh")
	if o.code == 0 || !strings.Contains(o.err, "errand setup") {
		t.Fatalf("one that is not set up: %d %q %q", o.code, o.out, o.err)
	}
	log := read(t, filepath.Join(guest, "log"))
	if !strings.Contains(log, "-p 2222") || !strings.Contains(log, "scp -q -P 2222 ") || !strings.Contains(log, "winbox:C:/errand/cache/main/bin/") {
		t.Errorf("how it was reached: %s", log)
	}
	if o = p.errand("", "envs"); !strings.Contains(o.out, "not built") || !strings.Contains(o.out, "windows-box:winbox -> vars") {
		t.Errorf("envs: %q %q", o.out, o.err)
	}
	if o = p.errand("", "setup", "win", "--dry-run"); o.code != 0 || !strings.Contains(o.err, "would set up the Windows at winbox") {
		t.Errorf("setup --dry-run: %d %q", o.code, o.err)
	}
	ready, _ := filepath.Glob(filepath.Join(guest, "home", "C:", "errand", "work", "proj", "job.sh"))
	if len(ready) != 1 {
		t.Errorf("the project was not pushed to the box")
	}
}

func TestAMachineThatIsBooted(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	guest := aWindows(t, p)
	base := filepath.Dir(p.root)
	qemu := filepath.Join(base, "qemu")
	_ = os.WriteFile(qemu, []byte("#!/bin/sh\necho \"$*\" >> "+base+"/qemu.log\nexec sleep 600\n"), 0o755)
	p.env = append(p.env, "ERRAND_QEMU="+qemu)
	p.write("errand-envs.py", `import errand
errand.envs[ "win" ] = errand.Env( [ errand.Windows( msvc = False, cpus = 1, ram = "64M", keep = "3s", fetch = [ "made" ] ), errand.Vars( { "WHERE": "there", "PATH": "/added:%PATH%" } ) ] )
`)
	p.write("job.sh", "#!/bin/sh\necho in $(pwd) $WHERE; case $PATH in /added:*/bin*) ;; *) exit 9;; esac; mkdir -p made; echo m > made/m.txt; exit 4\n")
	if o := p.errand("", "run", "-q", "--env", "win", "job.sh"); o.code != 1 || !strings.Contains(o.err, "windows is not installed yet") {
		t.Fatalf("one that is not installed: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("", "envs"); !strings.Contains(o.out, "not built") {
		t.Errorf("envs: %q %q", o.out, o.err)
	}
	dirs := kit.ReadJSON(filepath.Join(p.root, "errand-outputs", "state", "declared.json"))
	if dirs == nil {
		t.Fatal("the declarations were not kept")
	}
	// installed: the directory named by what it is installed from
	o := p.errand("", "run", "--env", "win", "--dry-run", "job.sh")
	_ = o
	name := imageOf(t, p)
	image := filepath.Join(p.root, "errand-outputs", "builds", "windows", name)
	_ = os.MkdirAll(image, 0o755)
	_ = os.WriteFile(filepath.Join(image, "ready"), []byte("today\n"), 0o644)

	o = p.errand("", "run", "-q", "--env", "win", "job.sh")
	if o.code != 4 || !strings.Contains(o.out, "E:/proj there") {
		t.Fatalf("a run in it: exit %d, %q %q", o.code, o.out, o.err)
	}
	if read(t, filepath.Join(p.root, "made", "m.txt")) != "m\n" {
		t.Errorf("what it is asked to bring back did not come")
	}
	if !o.hasResult || o.result.Status != "FAIL" {
		t.Errorf("its record, here: %+v", o.result)
	}
	if log := read(t, filepath.Join(base, "qemu.log")); strings.Count(log, "\n") != 1 || !strings.Contains(log, "-smp 1 -m 64M") || !strings.Contains(log, "hostfwd=tcp:127.0.0.1:") {
		t.Errorf("how it was booted: %s", log)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "windows") {
		t.Errorf("status while it is kept warm: %q", o.out)
	}
	if o = p.errand("", "queue"); !strings.Contains(o.out, "windows vm") {
		t.Errorf("the queue: %q", o.out)
	}
	// kept: the next command finds it up
	if o = p.errand("", "run", "-q", "--env", "win", "sh", "-c", "echo again"); o.code != 0 || o.out != "again\n" {
		t.Fatalf("a second run: %d %q %q", o.code, o.out, o.err)
	}
	if log := read(t, filepath.Join(base, "qemu.log")); strings.Count(log, "\n") != 1 {
		t.Errorf("booted again: %s", log)
	}
	// asked to stop
	if o = p.errand("", "vm", "stop"); o.code != 0 || !strings.Contains(o.out, "windows") {
		t.Errorf("vm stop: %d %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "vm", "stop"); o.code != 0 || !strings.Contains(o.out, "no virtual machine") {
		t.Errorf("vm stop, with none: %d %q %q", o.code, o.out, o.err)
	}
	// left unused, it goes by itself
	if o = p.errand("", "run", "-q", "--env", "win", "true"); o.code != 0 {
		t.Fatalf("booted again: %d %q", o.code, o.err)
	}
	if log := read(t, filepath.Join(base, "qemu.log")); strings.Count(log, "\n") != 2 {
		t.Errorf("not booted again: %s", log)
	}
	for until := time.Now().Add(15 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if o = p.errand("", "queue"); !strings.Contains(o.out, "windows vm") {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("it does not go: %q", o.out)
		}
	}
	// a batch cannot be left in it
	if o = p.errand("", "run", "--batch", "--env", "win", "true"); o.code == 0 || !strings.Contains(o.err, "--batch") {
		t.Errorf("--batch in a machine that is booted: %d %q", o.code, o.err)
	}
	_ = guest
}

// the machine errand boots, on another machine
func TestAMachineBootedElsewhere(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	aWindows(t, p)
	base := filepath.Dir(p.root)
	qemu := filepath.Join(base, "qemu")
	_ = os.WriteFile(qemu, []byte("#!/bin/sh\necho \"$*\" >> "+base+"/qemu.log\nexec sleep 600\n"), 0o755)
	p.env = append(p.env, "ERRAND_QEMU="+qemu)
	p.write("errand-envs.py", `import errand
errand.envs[ "win" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Windows( msvc = False, cpus = 1, ram = "64M", keep = "3s", data = None ) ] )
`)
	p.write("job.sh", "#!/bin/sh\necho in $(pwd)\n")
	name := imageOf(t, p)
	image := filepath.Join(p.far, "home", "copy", "proj", "errand-outputs", "builds", "windows", name)
	_ = os.MkdirAll(image, 0o755)
	_ = os.WriteFile(filepath.Join(image, "ready"), []byte("today\n"), 0o644)
	// ( the ssh of there is this one too )
	plain := read(t, filepath.Join(base, "ssh-plain"))
	_ = os.WriteFile(filepath.Join(base, "ssh-plain"), []byte(strings.Replace(plain, "SHELL=/bin/sh\n", "SHELL=/bin/sh ERRAND_SSH="+base+"/ssh ERRAND_SCP="+base+"/scp ERRAND_DIST="+base+" ERRAND_QEMU="+qemu+"\n", 1)), 0o755)
	o := p.errand("", "run", "-q", "--env", "win", "job.sh")
	if o.code != 0 || !strings.Contains(o.out, "C:/errand/proj") {
		t.Fatalf("a run in a machine booted elsewhere: exit %d, %q %q", o.code, o.out, o.err)
	}
	if !o.hasResult || o.result.Status != "PASS" {
		t.Errorf("its record, here: %+v", o.result)
	}
	if o = p.errand("", "vm", "stop"); o.code != 0 || !strings.Contains(o.out, "box") {
		t.Errorf("vm stop: %d %q %q", o.code, o.out, o.err)
	}
}

// imageOf: the image the project's environment boots from, as `envs --json` says it
func imageOf(t *testing.T, p *scene) string {
	t.Helper()
	o := p.errand("", "envs", "--json")
	i := strings.Index(o.out, `"name": "go-`)
	if i < 0 {
		t.Fatalf("envs --json: %d %q %q", o.code, o.out, o.err)
	}
	name := o.out[i+len(`"name": "`):]
	for k, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return name[:k]
		}
	}
	t.Fatal(fmt.Sprint("no image in ", o.out))
	return ""
}

func TestAWindowsIsNotInstalledAgainForWhatIsInside(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	aWindows(t, p)
	base := filepath.Dir(p.root)
	qemu := filepath.Join(base, "qemu")
	_ = os.WriteFile(qemu, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755)
	p.env = append(p.env, "ERRAND_QEMU="+qemu)
	p.write("errand-envs.py", `import errand
errand.envs[ "in" ] = errand.Env( [ errand.Windows( msvc = False, cpus = 1, ram = "64M", keep = "1s" ), errand.Uv( name = "in" ) ] )
`)
	image := filepath.Join(p.root, "errand-outputs", "builds", "windows", imageOf(t, p))
	_ = os.MkdirAll(image, 0o755)
	_ = os.WriteFile(filepath.Join(image, "ready"), []byte("today\n"), 0o644)
	if o := p.errand("", "envs"); !strings.Contains(o.out, "unknown ( the machine is not up )") {
		t.Errorf("not booted to be asked: %q %q", o.out, o.err)
	}
	o := p.errand("", "setup", "in", "--force", "--dry-run")
	if o.code != 0 || strings.Contains(o.err, "would install windows") || !strings.Contains(o.err, "uv:in") {
		t.Errorf("setup --force --dry-run: %d %q %q", o.code, o.out, o.err)
	}
	o = p.errand("", "setup", "in", "--force")
	if strings.Contains(o.err, "installing windows") || strings.Contains(o.err, "downloading") || !strings.Contains(o.err, "uv:in") {
		t.Errorf("setup --force: %d %q %q", o.code, o.out, o.err)
	}
}

func TestAWindowsIsInstalled(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	guest := aWindows(t, p)
	base := filepath.Dir(p.root)
	qemu := filepath.Join(base, "qemu")
	_ = os.WriteFile(qemu, []byte("#!/bin/sh\necho \"$*\" >> "+base+"/qemu.log\ncase \"$*\" in *media=cdrom*) cp -r build/media "+base+"/media; exit 0;; esac\nexec sleep 600\n"), 0o755)
	p.env = append(p.env, "ERRAND_QEMU="+qemu)
	p.write("win.iso", "an iso")
	p.write("more.ps1", "Write-Output more")
	p.write("errand-envs.py", `import errand
errand.envs[ "win" ] = errand.Env( [ errand.Windows( iso = "win.iso", key = "AAAA-BBBB", index = 2, cpus = 1, ram = "64M", keep = "1s", data = None,
    provision = [ "Write-Output 'a'", "more.ps1" ] ) ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "winbox" ), errand.WindowsBox( provision = [ "Write-Output 'b'", "more.ps1" ] ) ] )
`)
	image := filepath.Join(p.root, "errand-outputs", "builds", "windows", imageOf(t, p))
	old := filepath.Join(filepath.Dir(image), "go-old")
	_ = os.MkdirAll(old, 0o755)
	_ = os.WriteFile(filepath.Join(guest, "wrong"), nil, 0o644)
	if o := p.errand("", "setup", "win"); o.code == 0 || !strings.Contains(o.err, "went wrong: failed: no way") || kit.Exists(filepath.Join(p.cache(), "windows", imageOf(t, p), "ready")) {
		t.Fatalf("a setup that fails inside: %d %q", o.code, o.err)
	}
	_ = os.Remove(filepath.Join(guest, "wrong"))
	_ = os.RemoveAll(filepath.Join(base, "media"))
	o := p.errand("", "setup", "win")
	// installed in errand's directory of the machine, for every project that installs the same ( ADR 0044 )
	installed := filepath.Join(p.cache(), "windows", imageOf(t, p))
	if o.code != 0 || !strings.Contains(o.err, "windows is ready") || !kit.Exists(filepath.Join(installed, "ready")) || !kit.Exists(filepath.Join(installed, "base.qcow2")) {
		t.Fatalf("installed: %d %q", o.code, o.err)
	}
	if kit.Exists(filepath.Join(installed, "build")) || kit.Exists(old) || !kit.Exists(filepath.Join(installed, "key")) || kit.Exists(filepath.Join(image, "base.qcow2")) {
		t.Errorf("what is left, beside the image")
	}
	media := filepath.Join(base, "media")
	answers, script := read(t, filepath.Join(media, "autounattend.xml")), read(t, filepath.Join(media, "errand-setup.ps1"))
	for _, want := range []string{"<Key>AAAA-BBBB</Key>", "<Key>/IMAGE/INDEX</Key><Value>2</Value>"} {
		if !strings.Contains(answers, want) {
			t.Errorf("the answers do not say %q", want)
		}
	}
	for _, want := range []string{"vs_BuildTools.exe", "python/3.13", "Step 'provision: Write-Output ''a'''", "'errand-provision-1.ps1'", "setup.status 'ok'"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not say %q: %s", want, script)
		}
	}
	if strings.Contains(script, "@STEPS@") || strings.Contains(script, "@V@") || read(t, filepath.Join(media, "errand-provision-1.ps1")) != "Write-Output more" {
		t.Errorf("the media: %s", script)
	}
	if o = p.errand("", "setup", "win"); o.code != 0 || strings.Contains(o.err, "installing windows") {
		t.Errorf("installed again: %d %q", o.code, o.err)
	}
	// a Windows that is there
	if o = p.errand("", "setup", "box"); o.code != 0 || !strings.Contains(o.err, `python: C:\Python\python.exe, as a venv`) || !strings.Contains(o.err, "provision: more.ps1") {
		t.Fatalf("a box: %d %q", o.code, o.err)
	}
	if log := read(t, filepath.Join(guest, "log")); !strings.Contains(log, `"C:\Python\python.exe" -m venv --clear "C:\errand\venv"`) || !strings.Contains(log, "winbox:C:/errand/provision/1-more.ps1") {
		t.Errorf("what was done there: %s", log)
	}
	if o = p.errand("", "envs"); strings.Contains(o.out, "not built") {
		t.Errorf("envs, once it is: %q", o.out)
	}
}
