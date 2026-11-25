// Reaching another machine ( the guide's "Ssh: running elsewhere" ): one `ssh`, this program sent there once, and a
// link to it ( link.go ).
//
// **Nothing to install over there, and nothing to build.** The machine is asked what it is ( `uname` ); the errand
// for that system is taken from the distribution -- `errand-<os>-<arch>`, beside this program -- and sent, once, to
// `<cache>/bin/<its hash>/errand` there. A checkout of errand's sources builds it instead ( `go build`, for that
// system ), so that what answers over there is what is being worked on here.
//
// **One connection, kept**, by the agent of the machine the commands are typed on ( command/commands.go ): the first command to
// reach a host opens a link that the next ones go through, and nothing logs in again. `ERRAND_SSH_MASTER=1` has ssh keep
// its own as well ( `ControlMaster`, 15 minutes after its last use ).
//
// **Through an interactive shell**: what is started over there is started by `$SHELL -ic`, which reads the profile
// that puts micromamba or a module system on the `PATH`, and says where errand keeps its files ( `ERRAND_CACHE_DIR` ) --
// unless the place declares it: `Ssh( cache = "$SCRATCH/errand" )`.

package reach

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/system"
)

func (h HostSpec) getenv(name string) string {
	if h.Environ == nil {
		return os.Getenv(name)
	}
	out := ""
	for _, kv := range h.Environ {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			out = v
		}
	}
	return out
}

func (h HostSpec) said(format string, args ...any) {
	if text := fmt.Sprintf(format, args...); h.Say != nil {
		h.Say(text)
	} else {
		fmt.Fprintln(os.Stderr, text)
	}
}

// command: a program started on this machine to reach that one, in the surroundings of whoever asked
func (h HostSpec) command(argv []string) *exec.Cmd {
	cmd := exec.Command(argv[0], argv[1:]...)
	if h.Environ != nil {
		cmd.Env = h.Environ
		if path, err := kit.LookIn(argv[0], h.Environ, "."); err == nil {
			cmd.Path = path
		}
	}
	return cmd
}

func HostOf(l project.Layer) HostSpec {
	h := HostSpec{Host: l.String("host"), Options: l.Strings("options"), Cache: strings.TrimSpace(l.String("cache"))}
	if shared, ok := l["shared"].(bool); ok {
		h.Shared = &shared
	}
	if w, ok := l["windows"].(map[string]any); ok {
		h.Windows = true
		if w["vm"] != nil {
			h.VM = &VmWant{}
			_ = DecodeInto(w["vm"], h.VM)
			if via, ok := w["via"].(map[string]any); ok {
				h.VM.Via = &HostSpec{Host: kit.Text(via["host"]), Options: kit.Strs(via["options"]), Cache: kit.Text(via["cache"])}
			}
		}
	}
	return h
}

const (
	connectTimeout = 10  // seconds per address: a host that does not answer is said so, not waited for
	masterKept     = 900 // seconds a connection stays after its last use
)

func (h HostSpec) sshProgram() string {
	if p := h.getenv("ERRAND_SSH"); p != "" {
		return p
	}
	return "ssh"
}

// masterDir: where the kept connections' sockets are -- a short path ( a socket's is limited ), this user's alone
func masterDir() string {
	dir := fmt.Sprintf("/tmp/errand-ssh-%d", os.Getuid())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		return ""
	}
	return dir
}

// argv: ssh, its options, the host. Unless the options say otherwise: a connect timeout ( a host behind a filter hangs
// ssh without a word ) and keepalives ( a connection left half-open by a laptop asleep is said lost within a minute ).
func (h HostSpec) argv() []string {
	out := []string{h.sshProgram(), "-T"}
	out = append(out, h.Options...)
	said := strings.ToLower(strings.Join(h.Options, " "))
	add := func(key, value string) {
		if !strings.Contains(said, strings.ToLower(key)) {
			out = append(out, "-o", key+"="+value)
		}
	}
	add("ConnectTimeout", fmt.Sprint(connectTimeout))
	add("ServerAliveInterval", "15")
	add("ServerAliveCountMax", "4")
	if dir := masterDir(); dir != "" && h.getenv("ERRAND_SSH_MASTER") == "1" && !strings.Contains(said, "controlpath") {
		add("ControlMaster", "auto")
		out = append(out, "-o", "ControlPath="+dir+"/%C", "-o", fmt.Sprintf("ControlPersist=%d", masterKept))
	}
	return append(out, h.Host)
}

func (h HostSpec) environOr() []string {
	if h.Environ != nil {
		return h.Environ
	}
	return os.Environ()
}

// throughShell: a line as the user's interactive shell over there runs it. Whatever that shell is, it only has to
// run `sh -c '…'`.
func throughShell(script string) string {
	return "$SHELL -ic " + layer.Quote("sh -c "+layer.Quote(script))
}

// notReached: what ssh says, exit code 255, when it never got to run anything over there
var notReached = []string{"connect to host", "could not resolve hostname", "timed out", "no route to host",
	"connection refused", "connection closed by", "permission denied", "host key verification failed"}

var onWindows = []string{"is not recognized as an internal or external command", "is not recognized as the name of a cmdlet"}

// whyNot: the sentence to stop with when an ssh that ended never reached the host, or reached a Windows; ""
// when it did reach it
func (h HostSpec) whyNot(code int, said string) string {
	low := strings.ToLower(said)
	for _, w := range onWindows {
		if strings.Contains(low, w) {
			return fmt.Sprintf("%s is a Windows: reach it with [ Ssh( host = %q ), WindowsBox() ]", h.Host, h.Host)
		}
	}
	if code != 255 {
		return ""
	}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(said), "\n") {
		if strings.TrimSpace(l) != "" {
			last = strings.TrimSpace(l)
		}
	}
	if last == "" {
		last = "ssh failed"
	}
	for _, w := range notReached {
		if strings.Contains(low, w) {
			return fmt.Sprintf("cannot reach %s: %s\n  `ssh %s true` must work from this terminal. A cluster that only lets some addresses in needs the name\n  your ~/.ssh/config routes through a jump host ( ProxyJump ), or `jump = …` in its Ssh layer", h.Host, last, h.Host)
		}
	}
	return fmt.Sprintf("cannot reach %s: %s", h.Host, last)
}

// ── what the machine is ──────────────────────────────────────────────────────

// farInfo is what is known of a machine reached before: kept, so that a command asks it nothing again.
type farInfo struct {
	V     int    `json:"v"`
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Home  string `json:"home"`
	Cache string `json:"cache"` // errand's per-user directory there
}

func (h HostSpec) infoPath() string { return filepath.Join(kit.LocalCache(), "hosts", h.Key()+".json") }

func (h HostSpec) known() *farInfo {
	var i farInfo
	data, err := os.ReadFile(h.infoPath())
	if err != nil || json.Unmarshal(data, &i) != nil || i.V != 1 || i.OS == "" {
		return nil
	}
	return &i
}

const (
	probeMark   = "__errand__|"
	noCacheMark = "__errand_no_cache__|"
)

// declaredPath: a path declared for another machine, as a word its shell reads -- `~` and `$NAME` are that machine's, and a
// relative path is from the home directory there. A variable it does not have is an error, never an empty word.
func declaredPath(p string) (string, error) {
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		p = "$HOME" + p[1:]
	case !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "$"):
		p = "$HOME/" + p
	}
	var b strings.Builder
	b.WriteByte('"')
	for at := 0; at < len(p); {
		if m := pathVariable.FindStringSubmatch(p[at:]); m != nil {
			b.WriteString("${" + m[1] + m[2] + ":?not set there}")
			at += len(m[0])
			continue
		}
		c := p[at]
		switch {
		case c == '$':
			return "", fmt.Errorf("cache = %q: `$NAME` or `${NAME}` is a variable of that machine's; nothing else begins with `$`", p)
		case c == '"' || c == '`' || c == '\\':
			b.WriteByte('\\')
		case c == '\n':
			return "", fmt.Errorf("cache = %q: a path is one line", p)
		}
		b.WriteByte(c)
		at++
	}
	b.WriteByte('"')
	return b.String(), nil
}

var pathVariable = regexp.MustCompile(`^\$(?:([A-Za-z_]\w*)|\{([A-Za-z_]\w*)\})`)

// probe: the machine asked what it is, through its interactive shell
func (h HostSpec) probe() (*farInfo, error) {
	if h.Windows {
		return h.probeWindows()
	}
	cache, ready := `"${ERRAND_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/errand}"`, ""
	if h.Cache != "" { // declared: made if it is not there, and as it is, links followed
		at, err := declaredPath(h.Cache)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", h.Host, err)
		}
		ready = "c=" + at + "; if mkdir -p \"$c\" && [ -w \"$c\" ]; then c=$( cd \"$c\" && pwd -P ); else printf '\n" + noCacheMark + "%s|\n' \"$c\"; exit 1; fi; "
		cache = `"$c"`
	}
	script := ready + `printf '\n` + probeMark + `%s|%s|%s|%s|\n' "$(uname -s)" "$(uname -m)" "$HOME" ` + cache
	argv := append(h.argv(), throughShell(script))
	cmd := h.command(argv)
	var out, said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &said
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot reach %s: %v", h.Host, err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("cannot reach %s: ssh got in, then nothing came back for 120 s\n  the machine may be down or stuck ( a file system that hangs ), or the way to it dead", h.Host)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if at, ok := strings.CutPrefix(strings.TrimSpace(line), noCacheMark); ok {
			return nil, fmt.Errorf("%s: errand's directory there, %s ( `cache = %q` in its Ssh layer ), cannot be made or written: %s", h.Host, strings.TrimSuffix(at, "|"), h.Cache, kit.EndOf(said.String(), 200))
		}
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), probeMark); ok {
			parts := strings.Split(rest, "|")
			if len(parts) < 4 {
				break
			}
			i := &farInfo{V: 1, OS: strings.ToLower(parts[0]), Home: parts[2], Cache: parts[3]}
			switch strings.ToLower(parts[1]) {
			case "x86_64", "amd64":
				i.Arch = "amd64"
			case "aarch64", "arm64":
				i.Arch = "arm64"
			default:
				i.Arch = strings.ToLower(parts[1])
			}
			_ = os.MkdirAll(filepath.Dir(h.infoPath()), 0o755)
			_ = kit.WriteRecord(h.infoPath(), i)
			return i, nil
		}
	}
	if why := h.whyNot(cmd.ProcessState.ExitCode(), said.String()); why != "" {
		return nil, fmt.Errorf("%s", why)
	}
	return nil, fmt.Errorf("%s did not say what it is ( %s )", h.Host, kit.EndOf(said.String(), 200))
}

// ── the errand for it ────────────────────────────────────────────────────────

// programFor: the errand to send to a machine of that system -> its file here, one of those this errand was built with.
// Nothing is compiled when a command is typed, from a checkout either: what runs, here and there, is what was built.
func (h HostSpec) programFor(goos, arch string) (string, error) {
	name := "errand-" + goos + "-" + arch
	if goos == runtime.GOOS && arch == runtime.GOARCH {
		return kit.SelfPath(), nil
	}
	var looked []string
	dirs := []string{h.getenv("ERRAND_DIST")}
	for _, at := range system.Installed(h.getenv) { // beside it ( a build ), or in what pip put beside its `bin`
		dirs = append(dirs, at, filepath.Join(at, "..", "share", "errand"))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, n := range []string{name, name + ".exe"} {
			if at := filepath.Join(dir, n); kit.IsFile(at) {
				return at, nil
			}
		}
		looked = append(looked, dir)
	}
	return "", fmt.Errorf("this errand was built without a program for %s/%s ( %s, looked for in %s ); in a checkout, pip install . does", goos, arch, name, strings.Join(looked, ", "))
}

// ship: the program sent, into its place over there -- written beside, then renamed
func (h HostSpec) ship(local, dir string, i *farInfo) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	var packed bytes.Buffer
	z := gzip.NewWriter(&packed)
	_, _ = z.Write(data)
	_ = z.Close()
	h.said("  errand: sending itself to %s ( %s/%s, %.1f MB )", h.Host, i.OS, i.Arch, float64(packed.Len())/1e6)
	if h.Windows {
		return h.shipWindows(local, dir)
	}
	q := layer.Quote
	script := "mkdir -p " + q(dir) + " && gzip -dc > " + q(dir) + "/errand.$$ && chmod 755 " + q(dir) + "/errand.$$ && mv " + q(dir) + "/errand.$$ " + q(dir) + "/errand"
	argv := append(h.argv(), "sh -c "+q(script))
	cmd := h.command(argv)
	cmd.Stdin = &packed
	if said, err := cmd.CombinedOutput(); err != nil {
		if why := h.whyNot(cmd.ProcessState.ExitCode(), string(said)); why != "" {
			return fmt.Errorf("%s", why)
		}
		return fmt.Errorf("errand could not be written on %s, in %s: %s", h.Host, dir, kit.EndOf(string(said), 400))
	}
	return nil
}

// ── the link ─────────────────────────────────────────────────────────────────

// cacheSaid: what tells the program started there where errand's directory is, when that was declared -- and, by it, its agent
// and what the agent starts
func (h HostSpec) cacheSaid(cache string) string {
	if h.Cache == "" {
		return ""
	}
	return "export ERRAND_CACHE_DIR=" + layer.Quote(cache) + "; "
}

// start: the program over there started, its stream found past whatever the shell's profile printed
func (h HostSpec) start(program string) (*Link, int, string) {
	said_ := ""
	if h.Shared != nil { // ( the agent it starts, if it starts one, knows )
		said_ = "export ERRAND_SHARED=0; "
		if *h.Shared {
			said_ = "export ERRAND_SHARED=1; "
		}
	}
	said_ += h.cacheSaid(path.Dir(path.Dir(path.Dir(program)))) // ( <cache>/bin/<hash>/errand )
	argv := append(h.argv(), "$SHELL -ic "+layer.Quote(said_+"exec "+layer.Quote(program)+" there"))
	if h.Windows {
		argv = append(h.argv(), h.startWindows(program))
	}
	cmd := h.command(argv)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	said := &bytes.Buffer{}
	cmd.Stderr = said
	if err := cmd.Start(); err != nil {
		return nil, 255, err.Error()
	}
	hello := []byte(linkHello)
	seen, one := 0, make([]byte, 1)
	for read := 0; seen < len(hello) && read < 1<<20; read++ {
		if _, err := io.ReadFull(out, one); err != nil {
			_ = in.Close()
			_ = cmd.Wait()
			return nil, cmd.ProcessState.ExitCode(), said.String()
		}
		if one[0] == hello[seen] {
			seen++
		} else if seen = 0; one[0] == hello[0] {
			seen = 1
		}
	}
	if seen < len(hello) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, 1, "what answered is not errand"
	}
	l := NewLink(out, in, h.Host)
	l.cmd, l.said = cmd, said
	return l, 0, ""
}

// refused: what a bridge said in place of a link -- the errand that asks is behind the one in use over there
func (h HostSpec) refused(said string) error {
	if i := strings.Index(said, updateWord); i >= 0 {
		return fmt.Errorf("%s: %s", h.Host, strings.TrimSpace(strings.SplitN(said[i+len(updateWord):], "\n", 2)[0]))
	}
	return nil
}

// Connect: a link to the errand over there -- sent first when it is not there yet
func (h HostSpec) Connect() (*Link, error) {
	info := h.known()
	asked := false
	for {
		if info == nil {
			var err error
			if info, err = h.probe(); err != nil {
				return nil, err
			}
			asked = true
		}
		local, err := h.programFor(info.OS, info.Arch)
		if err != nil {
			return nil, err
		}
		sum, err := kit.HashOfFile(local)
		if err != nil {
			return nil, err
		}
		dir := info.Cache + "/bin/" + sum
		l, code, said := h.start(dir + "/errand")
		if l != nil {
			return l, nil
		}
		if err := h.refused(said); err != nil {
			return nil, err
		}
		if why := h.whyNot(code, said); why != "" {
			return nil, fmt.Errorf("%s", why)
		}
		if !asked { // what was known of it may be of before: asked again
			info = nil
			continue
		}
		if err := h.ship(local, dir, info); err != nil {
			return nil, err
		}
		if l, code, said = h.start(dir + "/errand"); l != nil {
			return l, nil
		}
		if err := h.refused(said); err != nil {
			return nil, err
		}
		if why := h.whyNot(code, said); why != "" {
			return nil, fmt.Errorf("%s", why)
		}
		return nil, fmt.Errorf("errand was sent to %s and does not start there ( %s ): %s", h.Host, dir+"/errand", kit.EndOf(said, 400))
	}
}
