// A machine errand boots ( the guide's "Windows"; ADR 0008 ): held by the agent of the machine that runs QEMU.
//
// As a service is: started when a command needs it, never twice, counted in the machine's queue -- firm while it is
// used, giving way while it is only kept warm --, and stopped when nobody uses it. A command holds it on a channel
// ( `vm_hold` ) for as long as it lasts, so that a command that goes, however it goes, lets go of it; a run of it that
// goes on by itself holds it too: the agent of the guest says it has one.
//
// What is asked of the guest's agent goes through here ( `vm_in` ): the guest listens on no address but this machine's
// own. What comes after it in a stack is the guest's business, as another machine's is.
//
// The image is installed by `vminstall.go`.

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/system"
)

const (
	vmBootWait  = 20 * time.Minute
	vmBusyEvery = 30 * time.Second
	vmUser      = "Administrator"
)

var vmGuestOptions = []string{"-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
	"-o", "LogLevel=ERROR", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}

// vmMachine is one, up or on its way.
type vmMachine struct {
	a     *Place
	want  reach.VmWant
	id    string
	image string // the project's directory for it, here: its data disk, what a boot leaves
	base  string // the installed image, shared by the projects that install the same ( ADR 0044 )
	since time.Time

	mu      sync.Mutex
	state   string // queued | booting | ready | warm | in use | stopping | stopped | failed: …
	port    int
	pid     int
	users   int
	used    bool // it served somebody
	last    time.Time
	guest   *reach.Link
	changed chan struct{}
	quit    chan struct{}
	ended   bool
	lock    *os.File
}

func vmID(image string) string {
	sum := sha256.Sum256([]byte(image))
	return "vm-" + hex.EncodeToString(sum[:])[:8]
}

func (m *vmMachine) over() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ended
}

// leaving: on its way down, or told to go
func (m *vmMachine) leaving() bool {
	select {
	case <-m.quit:
		return true
	default:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == "stopping"
}

func vmQemu() string {
	if p := os.Getenv("ERRAND_QEMU"); p != "" {
		return p
	}
	return "qemu-system-x86_64"
}

// accelerated: how this machine runs an x86-64 one, or why it does not
func accelerated() ([]string, error) {
	if os.Getenv("ERRAND_QEMU") != "" {
		return nil, nil // ( a stand-in: a test's )
	}
	if runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("%s is %s, and the Windows errand installs is x86-64: it would be emulated instruction by instruction. Reach an x86-64 machine that can: [ errand.Ssh( host = … ), errand.Windows() ]", queue.Host(), runtime.GOARCH)
	}
	if runtime.GOOS == "darwin" {
		return []string{"-accel", "hvf", "-cpu", "host"}, nil
	}
	st, err := os.Stat("/dev/kvm")
	if err != nil {
		return []string{"-accel", "tcg", "-cpu", "max"}, nil
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
		// the Hyper-V enlightenments: without them Windows spins on timers it believes are hardware
		return []string{"-accel", "kvm", "-cpu", "host,hv_relaxed,hv_vapic,hv_spinlocks=0x1fff,hv_time"}, nil
	}
	_ = st
	return nil, fmt.Errorf("/dev/kvm is there on %s, but you may not use it -- sudo usermod -aG kvm $USER, then log in again", queue.Host())
}

// vmTools: what booting a Windows takes, there or said
func vmTools(more ...string) error {
	tools := append([]string{vmQemu()}, more...)
	if os.Getenv("ERRAND_QEMU") == "" {
		tools = append(tools, "qemu-img")
	}
	for _, tool := range tools {
		if _, err := kit.LookIn(tool, os.Environ(), "."); err != nil {
			return fmt.Errorf("%s is not installed on %s: QEMU and OpenSSH are what boots a Windows and reaches it ( apt install qemu-system-x86 qemu-utils, brew install qemu )", tool, queue.Host())
		}
	}
	return nil
}

func escapedQemu(p string) string { return strings.ReplaceAll(p, ",", ",,") }

func (m *vmMachine) argv(accel []string, disk, data string, port int) []string {
	n, _, _, _ := kit.NeedsFrom([]string{"--ram", m.want.Ram})
	out := append([]string{vmQemu(), "-machine", "q35"}, accel...)
	out = append(out, "-smp", fmt.Sprint(m.want.Cpus), "-m", fmt.Sprintf("%dM", int(n.RAM)), "-rtc", "base=utc",
		"-monitor", "none", "-serial", "none", "-parallel", "none",
		// drivers Windows has: an AHCI disk, an e1000e card
		"-drive", "if=none,id=system,file="+escapedQemu(disk)+",format=qcow2,discard=unmap", "-device", "ide-hd,drive=system,bus=ide.0")
	if data != "" { // the disk that is kept: written to directly, never through an overlay
		out = append(out, "-drive", "if=none,id=data,file="+escapedQemu(data)+",format=qcow2,discard=unmap", "-device", "ide-hd,drive=data,bus=ide.2")
	}
	out = append(out, "-nic", fmt.Sprintf("user,model=e1000e,hostfwd=tcp:127.0.0.1:%d-:22", port))
	if vnc := os.Getenv("ERRAND_VM_VNC"); vnc != "" {
		return append(out, "-vnc", "127.0.0.1:"+vnc)
	}
	return append(out, "-display", "none")
}

func (m *vmMachine) say(state string) {
	m.mu.Lock()
	m.state = state
	close(m.changed)
	m.changed = make(chan struct{})
	m.mu.Unlock()
	m.a.touched()
}

func (m *vmMachine) host() reach.HostSpec {
	m.mu.Lock()
	port := m.port
	m.mu.Unlock()
	cache := reach.WindowsCache
	if m.want.Data != "" { // on the disk that is kept: what its agent holds outlives a boot, with the project
		cache = "E:/errand-cache"
	}
	return reach.HostSpec{Host: vmUser + "@127.0.0.1", Windows: true, Cache: cache, Booted: true, Say: func(string) {},
		Options: append([]string{"-p", fmt.Sprint(port), "-i", filepath.Join(m.base, "key")}, vmGuestOptions...)}
}

// reach: the link to the guest's agent, kept
func (m *vmMachine) reach() (*reach.Link, error) {
	m.mu.Lock()
	l := m.guest
	m.mu.Unlock()
	if l != nil {
		select {
		case <-l.Dead:
		default:
			return l, nil
		}
	}
	l, err := m.host().Connect()
	if err != nil {
		return nil, err
	}
	l.Where = "windows"
	m.mu.Lock()
	m.guest = l
	m.mu.Unlock()
	return l, nil
}

func powershell(script string) string {
	var b bytes.Buffer
	for _, u := range utf16.Encode([]rune(script)) {
		b.WriteByte(byte(u))
		b.WriteByte(byte(u >> 8))
	}
	return "powershell -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand " + base64.StdEncoding.EncodeToString(b.Bytes())
}

// run every boot, once the guest answers: a disk Windows Server has not seen is left offline, a new one is blank, and a
// letter is not promised from one boot to the next
const vmDataScript = `
$ErrorActionPreference = 'Stop'
$d = Get-Disk | Where-Object { -not $_.IsBoot -and -not $_.IsSystem } | Select-Object -First 1
if ( -not $d ) { throw 'no data disk' }
if ( $d.IsOffline )  { Set-Disk -Number $d.Number -IsOffline $false }
if ( $d.IsReadOnly ) { Set-Disk -Number $d.Number -IsReadOnly $false }
if ( ( Get-Disk -Number $d.Number ).PartitionStyle -eq 'RAW' ) {
    Initialize-Disk -Number $d.Number -PartitionStyle GPT
    New-Partition -DiskNumber $d.Number -UseMaximumSize -DriveLetter E | Out-Null
    Format-Volume -DriveLetter E -FileSystem NTFS -NewFileSystemLabel errand-data -Confirm:$false | Out-Null
} else {
    $p = Get-Partition -DiskNumber $d.Number | Where-Object Type -eq 'Basic' | Select-Object -First 1
    if ( $p.DriveLetter -ne 'E' ) { Set-Partition -DiskNumber $d.Number -PartitionNumber $p.PartitionNumber -NewDriveLetter E }
}
try { Add-MpPreference -ExclusionPath E:\ } catch { }
Write-Output 'errand-data-ok'
`

// what Windows has not written yet, written: the machine is thrown away rather than shut down, which to the disk that
// is kept is a power cut
const vmFlush = `Write-VolumeCache -DriveLetter E`

func (m *vmMachine) flush() {
	if m.want.Data != "" {
		_, _, _, _ = m.host().Talk(powershell(vmFlush), time.Minute)
	}
}

// vmAt: the machine asked for -- the one that is up, else one that starts ( when `boot` ) -> it, or the directory of
// the agent that holds it
func (a *Place) vmAt(want reach.VmWant, boot bool) (*vmMachine, string, error) {
	image := want.Dir()
	id := vmID(image)
	until := time.Now().Add(2 * time.Minute)
	a.mu.Lock()
	defer a.mu.Unlock()
again: // one that is going down is gone before another comes
	if m := a.machinesUp[id]; m != nil && !m.over() {
		if !m.leaving() {
			return m, "", nil
		}
		if boot && time.Now().Before(until) {
			a.mu.Unlock()
			time.Sleep(200 * time.Millisecond)
			a.mu.Lock()
			goto again
		}
	}
	for _, dir := range reach.Others(a.Root, a.Dir) {
		var o reach.OverviewDoc
		a.mu.Unlock()
		got, err := reach.AskAgent(dir, map[string]any{"op": "own"})
		a.mu.Lock()
		if err != nil || reach.DecodeInto(got["own"], &o) != nil {
			continue
		}
		for _, v := range o.VMs {
			if v.ID == id && v.State == "stopping" && boot && time.Now().Before(until) {
				a.mu.Unlock()
				time.Sleep(200 * time.Millisecond)
				a.mu.Lock()
				goto again
			}
			if v.ID == id {
				return nil, dir, nil
			}
		}
	}
	if m := a.machinesUp[id]; m != nil && !m.over() { // ( started while the others were asked )
		return m, "", nil
	}
	if !boot {
		return nil, "", errors.New("the virtual machine is not up")
	}
	if why := want.Installed(); why != "" {
		return nil, "", errors.New(why + ": errand setup")
	}
	accel, err := accelerated()
	if err != nil {
		return nil, "", err
	}
	if err := vmTools("ssh", "scp"); err != nil {
		return nil, "", err
	}
	live := filepath.Join(image, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		return nil, "", err
	}
	lock, err := os.OpenFile(filepath.Join(live, "lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, "", err
	}
	if system.TryLock(lock) != nil {
		lock.Close()
		if time.Now().Before(until) { // ( an agent that goes, and answers no more )
			a.mu.Unlock()
			time.Sleep(200 * time.Millisecond)
			a.mu.Lock()
			goto again
		}
		return nil, "", errors.New("this windows is being installed, or held by another errand")
	}
	m := &vmMachine{a: a, want: want, id: id, image: image, base: want.Base(), since: time.Now(), state: "queued", last: time.Now(),
		changed: make(chan struct{}), quit: make(chan struct{}), lock: lock}
	if a.machinesUp == nil {
		a.machinesUp = map[string]*vmMachine{}
	}
	a.machinesUp[id] = m
	go m.run(accel)
	return m, "", nil
}

func (m *vmMachine) record() string { return vmRecord(m.id) }

func (m *vmMachine) run(accel []string) {
	why := "stopped"
	live := filepath.Join(m.image, "live")
	overlay := filepath.Join(live, "overlay.qcow2")
	defer func() {
		_ = os.Remove(overlay)
		_ = os.Remove(m.record())
		m.mu.Lock()
		m.ended = true
		if m.guest != nil {
			m.guest.Close()
		}
		m.mu.Unlock()
		m.say(why)
		_ = system.Unlock(m.lock)
		m.lock.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-m.quit:
			cancel()
		case <-ctx.Done():
		}
	}()
	needs, _, _, err := kit.NeedsFrom([]string{"--cpus", fmt.Sprint(m.want.Cpus), "--ram", m.want.Ram})
	if err != nil {
		why = "failed: " + err.Error()
		return
	}
	granted, err := m.a.claim(ctx, queue.Request{Needs: needs, Label: "windows vm " + m.id[3:], Waiting: func(what string) { m.say("queued: " + what) }})
	if err != nil {
		if ctx.Err() == nil {
			why = "failed: " + err.Error()
		}
		return
	}
	defer granted.Release()
	if why = m.prepareDisks(live, overlay); why != "" {
		return
	}
	cmd, gone, said := m.start(accel, live, overlay)
	if said != "" {
		why = "failed: " + said
		return
	}
	defer func() { // thrown away rather than shut down: the next boot starts from the image again
		m.say("stopping")
		m.flush()
		qemuDown(cmd, gone)
	}()
	_ = os.Chmod(filepath.Join(m.base, "key"), 0o600) // ( ssh refuses one others may read: an ACL of the directory's, a copy )
	m.say("booting")
	if why = m.awaited(gone, live); why != "" {
		return
	}
	m.mu.Lock()
	m.last = time.Now()
	m.mu.Unlock()
	why = m.keepUp(granted, gone)
}

func (m *vmMachine) prepareDisks(live, overlay string) string {
	_ = os.Remove(overlay)
	if err := qemuImg("-b", filepath.Join(m.base, "base.qcow2"), "-F", "qcow2", overlay); err != nil {
		return "failed: " + err.Error()
	}
	now := time.Now() // when it was last booted: what tidies the images goes by it
	_ = os.WriteFile(filepath.Join(m.base, "used"), []byte(now.Format(time.UnixDate)+"\n"), 0o644)
	if m.want.Data != "" {
		if data := filepath.Join(m.image, "data.qcow2"); !kit.Exists(data) {
			if err := qemuImg(data, m.want.Data); err != nil {
				return "failed: " + err.Error()
			}
		}
	}
	return ""
}

// start: QEMU, its disk a layer over the image's
func (m *vmMachine) start(accel []string, live, overlay string) (*exec.Cmd, chan struct{}, string) {
	data := ""
	if m.want.Data != "" {
		data = filepath.Join(m.image, "data.qcow2")
	}
	cmd, port, gone, err := qemuUp(m.image, filepath.Join(live, "qemu.log"), m.id, func(port int) []string { return m.argv(accel, overlay, data, port) })
	if err != nil {
		return nil, nil, err.Error()
	}
	m.mu.Lock()
	m.port, m.pid = port, cmd.Process.Pid
	m.mu.Unlock()
	return cmd, gone, ""
}

// awaited: until the guest answers, and its disk is there -> why not
func (m *vmMachine) awaited(gone chan struct{}, live string) string {
	until := time.Now().Add(vmBootWait)
	for {
		out, _, code, err := m.host().Talk("echo errand-ok", 40*time.Second)
		if err == nil && code == 0 && strings.Contains(out, "errand-ok") {
			break
		}
		select {
		case <-gone:
			data, _ := os.ReadFile(filepath.Join(live, "qemu.log"))
			return "failed: QEMU stopped: " + kit.EndOf(string(data), 800)
		case <-m.quit:
			return "stopped"
		case <-time.After(2 * time.Second):
		}
		if time.Now().After(until) {
			return fmt.Sprintf("failed: windows did not answer within %s", reach.Span(vmBootWait.Seconds()))
		}
	}
	if m.want.Data != "" {
		out, said, _, err := m.host().Talk(powershell(vmDataScript), 5*time.Minute)
		if err != nil || !strings.Contains(out, "errand-data-ok") {
			return "failed: the disk that is kept could not be made E: " + kit.EndOf(out+said, 800)
		}
	}
	return ""
}

// keepUp: there, for as long as somebody needs it -> why it stops
func (m *vmMachine) keepUp(granted held, gone chan struct{}) string {
	keep, err := svc.Duration(m.want.Keep)
	if err != nil {
		keep = 300
	}
	g, _ := granted.(*queue.Granted)
	firm, busy, asked := true, false, time.Time{}
	for {
		m.mu.Lock()
		users, wait := m.users, m.changed
		m.mu.Unlock()
		if users == 0 && time.Since(asked) >= vmBusyEvery { // a run whose command went holds it as well
			busy, asked = m.guestBusy(), time.Now()
		}
		state := "warm"
		if users > 0 {
			state, busy, asked = "in use", false, time.Time{}
		} else if busy {
			state = "busy"
		}
		m.mu.Lock()
		if state != "warm" {
			m.last, m.used = time.Now(), true
		}
		idle, served, was := time.Since(m.last).Seconds(), m.used, m.state
		m.mu.Unlock()
		if was != state {
			m.say(state)
			wait = nil
		}
		if g != nil && firm != (state != "warm") {
			firm = state != "warm"
			g.SetPreemptible(!firm)
		}
		if state == "warm" {
			switch {
			case g != nil && g.Evicted():
				return "gave way to a computation"
			case !served && idle > 10:
				return "stopped: nobody was left to use it"
			case idle > keep:
				return fmt.Sprintf("stopped: unused for %s", reach.Span(keep))
			}
		}
		select {
		case <-gone:
			return "QEMU stopped by itself"
		case <-m.quit:
			return "stopped"
		case <-wait:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (m *vmMachine) guestBusy() bool {
	m.mu.Lock()
	served := m.used
	m.mu.Unlock()
	if !served {
		return false
	}
	l, err := m.reach()
	if err != nil {
		return false
	}
	got, err := l.Ask(map[string]any{"op": "hello"})
	return err == nil && kit.Num(got["running"], 0) > 0
}

// hold: the machine, up and held for somebody, what it is at said as it changes -> what lets go of it
func (m *vmMachine) hold(gone <-chan struct{}, say func(string)) (func(), error) {
	m.mu.Lock()
	m.users++
	m.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			m.users--
			m.last = time.Now()
			m.mu.Unlock()
			go m.flush()
		})
	}
	told := ""
	for {
		m.mu.Lock()
		state, ended, wait := m.state, m.ended, m.changed
		m.mu.Unlock()
		switch {
		case ended:
			release()
			return nil, fmt.Errorf("the windows machine on %s did not come up: %s", queue.Host(), state)
		case state == "ready" || state == "warm" || state == "in use" || state == "busy":
			return release, nil
		}
		if state != told {
			told = state
			switch {
			case state == "booting":
				say("  booting windows on " + queue.Host())
			case strings.HasPrefix(state, "queued: "):
				say("  the windows machine waits for " + state[8:])
			}
		}
		select {
		case <-wait:
		case <-gone:
			release()
			return nil, errors.New("whoever asked went")
		}
	}
}

// serveVM: what is asked of the machines this one boots
func (a *Place) serveVM(req map[string]any, f *reach.FarChan) {
	var want reach.VmWant
	_ = reach.DecodeInto(req["vm"], &want)
	note := func(text string) {
		b, _ := json.Marshal(map[string]any{"say": text})
		f.Out(kit.NOTE, b)
	}
	switch req["op"] {
	case "vm_state":
		f.Reply(map[string]any{"ok": true, "missing": want.Installed()})
	case "vm_stop":
		f.Reply(map[string]any{"ok": true, "stopped": a.stopVMs(kit.Text(req["id"]), req["own"] != true)})
	case "vm_hold":
		boot := req["boot"] != false
		m, other, err := a.vmAt(want, boot)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		if other != "" {
			a.pass(other, req, f)
			return
		}
		release, err := m.hold(f.Gone, note)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		defer release()
		f.Reply(map[string]any{"ok": true})
		for {
			select {
			case <-f.Gone:
				return
			case <-f.Inbox:
			}
		}
	case "vm_in":
		m, other, err := a.vmAt(want, false)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		if other != "" {
			a.pass(other, req, f)
			return
		}
		inner, _ := req["req"].(map[string]any)
		l, err := m.reach()
		if err != nil {
			f.Fail("%v", err)
			return
		}
		m.mu.Lock()
		m.users++
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			m.users--
			m.last = time.Now()
			m.mu.Unlock()
		}()
		passOn(l, inner, f)
	case "vm_install":
		a.vmInstall(req, want, f)
	}
}

// passOn: a request passed on along a link, both ways, until whoever asked is through with it
func passOn(l *reach.Link, req map[string]any, f *reach.FarChan) {
	c := l.Open(req)
	over := make(chan struct{})
	go func() {
		defer close(over)
		for {
			kind, payload, err := c.Recv()
			if err != nil {
				b, _ := json.Marshal(map[string]any{"error": "the machine it goes through went: " + err.Error()})
				f.Out(kit.REPLY, b)
				return
			}
			f.Out(kind, payload)
		}
	}()
	for {
		select {
		case m := <-f.Inbox:
			_ = c.Send(m.Kind, m.Payload)
		case <-over:
			c.Abandon()
			return
		case <-f.Gone:
			select {
			case <-f.Said: // told to stop: so is the far one
				c.Close()
			default:
				switch kit.Text(req["op"]) {
				case "setup", "launch": // silence is not a stop
					c.Abandon()
				default:
					c.Close()
				}
			}
			return
		}
	}
}

// stopVMs: those of this agent, and of the others of this machine when `all` -> what they were
func (a *Place) stopVMs(id string, all bool) []string {
	out := []string{}
	a.mu.Lock()
	var mine []*vmMachine
	for _, m := range a.machinesUp {
		if !m.ended && (id == "" || m.id == id) {
			mine = append(mine, m)
		}
	}
	a.mu.Unlock()
	for _, m := range mine {
		out = append(out, "windows "+m.id[3:])
		m.mu.Lock()
		select {
		case <-m.quit:
		default:
			close(m.quit)
		}
		m.mu.Unlock()
	}
	if all {
		for _, dir := range reach.Others(a.Root, a.Dir) {
			if got, err := reach.AskAgent(dir, map[string]any{"op": "vm_stop", "id": id, "own": true}); err == nil {
				out = append(out, kit.Strs(got["stopped"])...)
			}
		}
	}
	for _, m := range mine { // down, or on its way for good
		for until := time.Now().Add(90 * time.Second); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
			m.mu.Lock()
			ended := m.ended
			m.mu.Unlock()
			if ended {
				break
			}
		}
	}
	return out
}

// vms: the machines this agent holds
func (a *Place) vms() []reach.VmRow {
	a.mu.Lock()
	var all []*vmMachine
	for _, m := range a.machinesUp {
		all = append(all, m)
	}
	a.mu.Unlock()
	out := []reach.VmRow{}
	for _, m := range all {
		m.mu.Lock()
		if !m.ended {
			out = append(out, reach.VmRow{ID: m.id, Text: fmt.Sprintf("windows %s  %d cpus, %s", m.id[3:], m.want.Cpus, m.want.Ram), State: m.state, Pid: m.pid, Since: kit.Seconds(m.since)})
		}
		m.mu.Unlock()
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

// vmInstall: the image made, or found made -- under a place in this machine's queue
func (a *Place) vmInstall(req map[string]any, want reach.VmWant, f *reach.FarChan) {
	note := func(text string) {
		b, _ := json.Marshal(map[string]any{"say": text})
		f.Out(kit.NOTE, b)
	}
	f.Reply(map[string]any{"ok": true})
	code := 1
	defer func() {
		b, _ := json.Marshal(map[string]any{"code": code})
		f.Out(kit.EXIT, b)
	}()
	force, _ := req["force"].(bool)
	dry, _ := req["dry_run"].(bool)
	if want.Installed() == "" && !force {
		code = 0
		return
	}
	if dry {
		note("  would install windows " + want.Name + " on " + queue.Host())
		code = 0
		return
	}
	if _, err := accelerated(); err != nil {
		note("errand: " + err.Error())
		return
	}
	a.stopVMs(vmID(want.Dir()), true)
	live := filepath.Join(want.Dir(), "live") // nobody boots it while it is made
	lock, err := os.OpenFile(filepath.Join(live, "lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil && os.MkdirAll(live, 0o755) == nil {
		lock, err = os.OpenFile(filepath.Join(live, "lock"), os.O_RDWR|os.O_CREATE, 0o644)
	}
	if err != nil {
		note("errand: " + err.Error())
		return
	}
	defer lock.Close()
	if system.TryLock(lock) != nil {
		note("errand: this windows is in use, or being installed, by another errand on " + queue.Host())
		return
	}
	defer func() { _ = system.Unlock(lock) }()
	needs, _, _, err := kit.NeedsFrom([]string{"--cpus", fmt.Sprint(want.Cpus), "--ram", want.Ram})
	if err != nil {
		note("errand: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // told to stop, it stops; a connection that only drops stops nothing
		select {
		case <-f.Said:
			cancel()
		case <-ctx.Done():
		}
	}()
	a.mu.Lock()
	a.busy++
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy--
		a.idleSince = time.Now()
		a.mu.Unlock()
	}()
	granted, err := a.claim(ctx, queue.Request{Needs: needs, Label: "installing windows", Waiting: func(what string) { note("  waiting: " + what) }})
	if err != nil {
		note("errand: " + err.Error())
		return
	}
	defer granted.Release()
	if err := a.installWindows(ctx, want, ReadWindowsSetup(req["setup"]), kit.Strs(req["wanted"]), note); err != nil {
		note("  " + err.Error())
		return
	}
	code = 0
}
