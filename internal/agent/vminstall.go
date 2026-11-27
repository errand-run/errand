// What is installed on a Windows ( the guide's "Windows" ): the image of a machine errand boots, made from an ISO by an
// unattended installation, checked, then kept; and what errand keeps on a Windows that is somebody's, all of it in
// `C:\errand`. The recipes are the files of `windows/`.

package agent

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/download"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/system"
)

const (
	vmInstallWait = 4 * time.Hour
	// Windows Server 2025, evaluation edition, English: 180 days without a key, and a download Microsoft hands out without
	// asking anything. Its image 1 is Standard, Server Core: no desktop, which nothing run over ssh needs.
	evaluationISO = "https://go.microsoft.com/fwlink/?linkid=2293312&clcid=0x409&culture=en-us&country=us"

	boxErrand  = `C:\errand`
	boxVenv    = boxErrand + `\venv`
	boxReady   = boxErrand + `\ready`
	boxVcvars  = `C:\BuildTools\VC\Auxiliary\Build\vcvars64.bat`
	boxPrelude = "$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'\n"
)

//go:embed windows
var windowsFiles embed.FS

func windowsFile(name string) string {
	data, _ := windowsFiles.ReadFile("windows/" + name)
	return string(data)
}

// provisionStep is one more thing to do inside: a line of PowerShell, or a script of the project
type provisionStep struct {
	Line string `json:"line"`
	Name string `json:"name"`
	Text string `json:"text"`
}

// windowsSetup is what a Windows is installed with
type windowsSetup struct {
	ISO       string          `json:"iso"`
	Key       string          `json:"key"`
	Index     int             `json:"index"`
	Disk      string          `json:"disk"`
	MSVC      *bool           `json:"msvc"`
	Python    string          `json:"python"`
	Provision []provisionStep `json:"provision"`
}

func ReadWindowsSetup(v any) windowsSetup {
	var s windowsSetup
	_ = reach.DecodeInto(v, &s)
	return s
}

// autounattend: what Windows Setup reads instead of asking
func (s windowsSetup) autounattend() string {
	product := ""
	if s.Key != "" {
		product = "<ProductKey><Key>" + s.Key + "</Key><WillShowUI>OnError</WillShowUI></ProductKey>"
	}
	return strings.NewReplacer("@INDEX@", fmt.Sprint(max(s.Index, 1)), "@PRODUCT@", product).Replace(windowsFile("autounattend.xml"))
}

// script: the first logon, unattended -- sshd first, so that a failure further down can still be looked at; then the
// tools; a line saying how it went; and off, which is how the host knows the installation is over
func (s windowsSetup) script() string {
	var steps strings.Builder
	if s.MSVC == nil || *s.MSVC {
		steps.WriteString(windowsFile("msvc.ps1"))
	}
	version := s.Python
	if version == "" {
		version = project.GuestPython
	}
	steps.WriteString(strings.ReplaceAll(windowsFile("python.ps1"), "@V@", version))
	label := func(text string) string { return "'" + strings.ReplaceAll(text, "'", "''") + "'" }
	for i, p := range s.Provision {
		if p.Name == "" {
			short := []rune(p.Line)
			fmt.Fprintf(&steps, "\n    Step %s\n    %s", label("provision: "+string(short[:min(len(short), 40)])), p.Line)
		} else {
			fmt.Fprintf(&steps, "\n    Step %s\n    & ( Join-Path $media 'errand-provision-%d.ps1' )", label(p.Name), i)
		}
	}
	return strings.Replace(windowsFile("setup.ps1"), "@STEPS@", steps.String(), 1)
}

// qemuUp: QEMU on a port of this machine that is free, its words in `log` -> it, its port, what is closed when it ends;
// or why not. It is written down under `name`, so that one an agent that was killed left is found.
func qemuUp(dir, log, name string, argv func(port int) []string) (*exec.Cmd, int, chan struct{}, error) {
	for try := 0; try < 5; try++ {
		port := 20000 + rand.Intn(40000)
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
			continue
		} else {
			l.Close()
		}
		words := argv(port)
		path, err := kit.LookIn(words[0], os.Environ(), dir)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("%s: %v", words[0], err)
		}
		out, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, 0, nil, err
		}
		cmd := &exec.Cmd{Path: path, Args: words, Env: os.Environ(), Dir: dir, Stdout: out, Stderr: out, SysProcAttr: system.Apart()}
		err = cmd.Start()
		out.Close()
		if err != nil {
			return nil, 0, nil, err
		}
		_ = os.MkdirAll(filepath.Join(kit.LocalCache(), "vm"), 0o755)
		_ = kit.WriteWholeJSON(vmRecord(name), map[string]any{"pid": cmd.Process.Pid, "start": fmt.Sprint(kit.Started(cmd.Process.Pid)), "agent": os.Getpid()})
		gone := make(chan struct{})
		go func() { _ = cmd.Wait(); close(gone) }()
		select {
		case <-gone: // a port taken or a disk locked: QEMU says so at once
			said, _ := os.ReadFile(log)
			if cmd.ProcessState.Success() {
				return cmd, port, gone, nil
			}
			if s := string(said); strings.Contains(s, "hostfwd") || strings.Contains(s, "host forwarding") {
				continue
			}
			return nil, 0, nil, errors.New("QEMU did not start: " + kit.EndOf(string(said), 800))
		case <-time.After(1500 * time.Millisecond):
		}
		return cmd, port, gone, nil
	}
	return nil, 0, nil, errors.New("no free port for ssh into the machine, five tries")
}

func qemuDown(cmd *exec.Cmd, gone chan struct{}) {
	_ = system.SignalGroup(cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-gone:
	case <-time.After(20 * time.Second):
		_ = system.SignalGroup(cmd.Process.Pid, syscall.SIGKILL)
		<-gone
	}
}

func vmRecord(id string) string { return filepath.Join(kit.LocalCache(), "vm", id+".json") }

// qemuImg: a disk, made
func qemuImg(args ...string) error {
	if os.Getenv("ERRAND_QEMU") != "" { // ( a stand-in: a test's )
		for _, a := range args {
			if strings.HasSuffix(a, ".qcow2") && !kit.Exists(a) {
				return os.WriteFile(a, nil, 0o644)
			}
		}
		return nil
	}
	if said, err := exec.Command("qemu-img", append([]string{"create", "-q", "-f", "qcow2"}, args...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("a disk: %s", kit.EndOf(string(said), 400))
	}
	return nil
}

// installWindows: the image of a machine errand boots, made in `want.dir()`; what is done said a line at a time. The
// machine's resources are held by whoever asks.
func (a *Place) installWindows(ctx context.Context, want reach.VmWant, s windowsSetup, wanted []string, say func(string)) error {
	accel, err := accelerated()
	if err != nil {
		return err
	}
	if len(accel) > 1 && accel[1] == "tcg" {
		say("  no hardware virtualization on " + queue.Host() + " ( /dev/kvm ): this will take HOURS")
	}
	if err := vmTools("ssh", "ssh-keygen"); err != nil {
		return err
	}
	// installed in errand's directory of this machine, for every project that installs the same ( ADR 0044 ): what is
	// the project's ( its data disk ) stays in its own
	root, image := reach.CopyRoot(want.Root), filepath.Join(system.Cache(), "windows", want.Name)
	if err := os.MkdirAll(image, 0o755); err != nil {
		return err
	}
	installing, err := os.OpenFile(filepath.Join(image, "installing"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer installing.Close()
	if system.TryLock(installing) != nil {
		return errors.New("this windows is being installed for another project on " + queue.Host() + ": once it is, it serves both")
	}
	defer system.Unlock(installing)
	downloads := filepath.Join(filepath.Dir(image), ".downloads")
	iso, err := windowsISO(ctx, s.ISO, root, downloads, say)
	if err != nil {
		return err
	}
	work := filepath.Join(image, "build")
	media, disk := filepath.Join(work, "media"), filepath.Join(work, "disk.qcow2")
	_ = os.RemoveAll(work)
	defer os.RemoveAll(work)
	if err := os.MkdirAll(media, 0o755); err != nil {
		return err
	}
	key := filepath.Join(image, "key")
	if !kit.Exists(key) {
		if said, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "errand-vm", "-f", key).CombinedOutput(); err != nil {
			return fmt.Errorf("the guest's key: %s", kit.EndOf(string(said), 400))
		}
	}
	_ = os.Chmod(key, 0o600)
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	files := map[string]string{"errand_key.pub": string(public), "autounattend.xml": s.autounattend(), "errand-setup.ps1": s.script()}
	for i, p := range s.Provision {
		if p.Name != "" {
			files[fmt.Sprintf("errand-provision-%d.ps1", i)] = p.Text
		}
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(media, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	size := s.Disk
	if size == "" {
		size = "64G"
	}
	if err := qemuImg(disk, size); err != nil {
		return err
	}

	say("  installing windows on " + queue.Host() + " -- 20 to 60 minutes, with nothing to watch ( ERRAND_VM_VNC=1 to watch on vnc://127.0.0.1:5901 there )")
	m := &vmMachine{a: a, want: want, id: vmID(want.Dir()), image: want.Dir(), base: image}
	name := m.id + "-install"
	defer os.Remove(vmRecord(name))
	began := time.Now()
	cmd, _, gone, err := qemuUp(image, filepath.Join(work, "qemu.log"), name, func(port int) []string {
		// from the ISO once: the reboots in the middle of the installation land on the disk it is writing. The media are a
		// directory seen as a USB stick, where Windows Setup looks for `autounattend.xml` of its own accord; read-only, and
		// said so: a USB disk over a read-only node is refused unless the drive declares it.
		return append(m.argv(accel, disk, "", port),
			"-drive", "if=none,id=setup,file="+escapedQemu(iso)+",media=cdrom,readonly=on", "-device", "ide-cd,drive=setup,bus=ide.1", "-boot", "once=d",
			"-drive", "if=none,id=media,file=fat:"+escapedQemu(media)+",format=raw,readonly=on", "-device", "qemu-xhci", "-device", "usb-storage,drive=media,removable=on")
	})
	if err != nil {
		return err
	}
	report := time.NewTicker(10 * time.Minute)
	defer report.Stop()
	late := time.After(vmInstallWait)
	for over := false; !over; { // Windows powers the machine off when it is done
		select {
		case <-gone:
			over = true
		case <-report.C:
			say(fmt.Sprintf("    still installing, %.0f min", time.Since(began).Minutes()))
		case <-late:
			qemuDown(cmd, gone)
			return fmt.Errorf("the installation did not finish in %s", reach.Span(vmInstallWait.Seconds()))
		case <-ctx.Done():
			qemuDown(cmd, gone)
			return errors.New("stopped")
		}
	}
	base := filepath.Join(image, "base.qcow2")
	_ = os.Remove(filepath.Join(image, "ready")) // ( what was there is not what it says any more )
	if err := os.Rename(disk, base); err != nil {
		return err
	}
	say(fmt.Sprintf("  installed in %.0f min; checking that it answers", time.Since(began).Minutes()))
	if err := m.checkInstalled(ctx, accel, work, name); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(image, "ready"), []byte(time.Now().Format(time.UnixDate)+"\n"), 0o644); err != nil {
		return err
	}
	say("  windows is ready on " + queue.Host() + ": " + image)
	pruneImages(filepath.Dir(want.Dir()), append([]string{want.Name}, wanted...), say) // the project's own, of images it no longer declares
	_ = os.RemoveAll(downloads)
	return nil
}

// checkInstalled: it is booted once, the way every run will boot it, and asked how its setup went
func (m *vmMachine) checkInstalled(ctx context.Context, accel []string, work, name string) error {
	overlay := filepath.Join(work, "check.qcow2")
	if err := qemuImg("-b", filepath.Join(m.base, "base.qcow2"), "-F", "qcow2", overlay); err != nil {
		return err
	}
	cmd, port, gone, err := qemuUp(m.base, filepath.Join(work, "check.log"), name, func(port int) []string { return m.argv(accel, overlay, "", port) })
	if err != nil {
		return err
	}
	defer qemuDown(cmd, gone)
	m.port = port
	h := m.host()
	for until := time.Now().Add(vmBootWait); ; {
		if out, _, code, err := h.Talk("echo errand-ok", 40*time.Second); err == nil && code == 0 && strings.Contains(out, "errand-ok") {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("stopped")
		case <-gone:
			return errors.New("installed, but it did not boot")
		case <-time.After(3 * time.Second):
		}
		if time.Now().After(until) {
			return errors.New("installed, but sshd never answered: the first logon script did not run, or failed before sshd -- ERRAND_VM_VNC=1 and errand setup --force to watch it")
		}
	}
	out, _, _, _ := h.Talk(`type C:\errand\setup.status`, time.Minute)
	if status := strings.TrimSpace(out); status != "ok" {
		if status == "" {
			status = "no status"
		}
		log, _, _, _ := h.Talk(`type C:\errand-setup.log`, time.Minute)
		return fmt.Errorf("the setup inside windows went wrong: %s\n%s", status, kit.EndOf(log, 1500))
	}
	return nil
}

// windowsISO: the ISO, a path of the project there, or a download beside the images -- removed once the image is installed
func windowsISO(ctx context.Context, iso, root, downloads string, say func(string)) (string, error) {
	if iso == "" {
		iso = evaluationISO
	}
	if !strings.Contains(iso, "://") {
		path := iso
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("no ISO at %s on %s", iso, queue.Host())
		}
		return path, nil
	}
	sum := sha256.Sum256([]byte(iso))
	target := filepath.Join(downloads, hex.EncodeToString(sum[:])[:16]+".iso")
	if kit.Exists(target) {
		return target, nil
	}
	say("  downloading " + iso + " on " + queue.Host())
	var err error
	for try := 0; try < 3; try++ {
		last := time.Now()
		var digest string
		digest, err = download.FetchURL(iso, downloads, "windows.iso", "", os.Environ(), func(done, total int64) {
			if time.Since(last) > 30*time.Second && total > 0 {
				last = time.Now()
				say(fmt.Sprintf("    %s of %s", caches.Human(float64(done)), caches.Human(float64(total))))
			}
		})
		if err == nil {
			if err = os.Rename(filepath.Join(downloads, digest, "windows.iso"), target); err == nil {
				return target, nil
			}
		}
		if ctx.Err() != nil {
			return "", errors.New("stopped")
		}
	}
	return "", fmt.Errorf("the ISO could not be downloaded on %s ( %v ) -- download it by hand and declare it: errand.Windows( iso = 'path/to/windows.iso' )", queue.Host(), err)
}

// pruneImages: the images made for the project that no environment declares any more
func pruneImages(images string, keep []string, say func(string)) {
	dirs, _ := filepath.Glob(filepath.Join(images, "go-*"))
	wanted := map[string]bool{}
	for _, k := range keep {
		wanted[k] = true
	}
	for _, d := range dirs {
		if wanted[filepath.Base(d)] || !kit.IsDir(d) {
			continue
		}
		if lock, err := os.OpenFile(filepath.Join(d, "live", "lock"), os.O_RDWR, 0o644); err == nil {
			held := system.TryLock(lock) != nil
			if !held {
				_ = system.Unlock(lock)
			}
			lock.Close()
			if held {
				continue // booted: in use
			}
		}
		if os.RemoveAll(d) == nil {
			say("  an image no environment declares any more was removed: " + filepath.Base(d))
		}
	}
}

// ── a Windows that is somebody's ─────────────────────────────────────────────

var boxPythonSaid = regexp.MustCompile(`(?m)^errand-python=(.*?)\s*$`)

// SetUpBox: what errand keeps on a Windows that is there -- a Python, as a venv of its own; the provisioning; then the
// mark that says so. Nothing on its PATH, nothing in its registry: deleting `C:\errand` undoes it.
func SetUpBox(h reach.HostSpec, s windowsSetup, mark string, say func(string)) error {
	long := 30 * time.Minute
	if out, _, code, err := h.Talk("echo errand-ok", time.Minute); err != nil || code != 0 || !strings.Contains(out, "errand-ok") {
		return fmt.Errorf("cannot reach %s: `ssh %s echo ok` must work from this terminal, and its shell be cmd.exe ( Windows' OpenSSH default )", h.Host, h.Host)
	}
	if _, err := h.Must(`for %d in ( "`+boxErrand+`" "`+boxErrand+`\bin" "`+boxErrand+`\work" "`+boxReady+`" "`+boxErrand+`\provision" ) do @if not exist %d mkdir %d`, `making C:\errand`, time.Minute); err != nil {
		return err
	}
	script := windowsFile("find-python.ps1")
	if s.Python != "" {
		say("    python " + s.Python + ", by uv, into " + boxErrand)
		script = strings.ReplaceAll(windowsFile("box-python.ps1"), "@V@", s.Python)
	}
	out, said, code, err := h.Talk(powershell(boxPrelude+script), long)
	if err != nil {
		return err
	}
	found := boxPythonSaid.FindStringSubmatch(out)
	switch {
	case found == nil && s.Python != "":
		return fmt.Errorf("uv installed no python %s on %s ( exit %d ): %s", s.Python, h.Host, code, kit.EndOf(said+out, 600))
	case found == nil:
		return fmt.Errorf("no Python on %s ( py -3, python ): install one, or say WindowsBox( python = %q ) and errand installs it into C:\\errand with uv", h.Host, "3.13.1")
	}
	python := strings.TrimSpace(found[1])
	say("    python: " + python + ", as a venv in " + boxVenv)
	if _, err := h.Must(`"`+python+`" -m venv --clear "`+boxVenv+`"`, "making a venv of "+python, long); err != nil {
		return err
	}
	if s.MSVC != nil && *s.MSVC {
		if _, err := h.Must(`if exist "`+boxVcvars+`" ( exit 0 ) else ( exit 3 )`, "msvc = True: the Build Tools ( "+boxVcvars+" )", time.Minute); err != nil {
			return err
		}
	}
	for i, p := range s.Provision {
		line, what := powershell(boxPrelude+p.Line), p.Line
		if p.Name != "" {
			what = p.Name
			tmp, err := os.MkdirTemp("", "errand-box-")
			if err != nil {
				return err
			}
			local, remote := filepath.Join(tmp, p.Name), fmt.Sprintf("C:/errand/provision/%d-%s", i, p.Name)
			if err = os.WriteFile(local, []byte(p.Text), 0o644); err == nil {
				err = h.Put(local, remote)
			}
			os.RemoveAll(tmp)
			if err != nil {
				return err
			}
			line = `powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "` + reach.Backslashed(remote) + `"`
		}
		short := []rune(what)
		say("    provision: " + string(short[:min(len(short), 60)]))
		out, said, code, err := h.Talk(`set "PATH=`+boxVenv+`\Scripts;%PATH%" && `+line, 2*time.Hour)
		if err != nil {
			return err
		}
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				say("      " + l)
			}
		}
		if code != 0 {
			return fmt.Errorf("provision %d ( %s ) failed on %s: %s", i+1, string(short[:min(len(short), 40)]), h.Host, kit.EndOf(said, 600))
		}
	}
	_, err = h.Must(`echo ready> "`+reach.Backslashed(mark)+`"`, "marking it ready", time.Minute)
	return err
}
