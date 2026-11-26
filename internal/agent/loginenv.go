// The surroundings a server starts in ( ADR 0040 ): those of a login shell of this machine. Not this agent's, which depend on
// what started it: a terminal, an ssh, another machine's errand.

package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/system"
)

const (
	loginKept = time.Minute // what it says is read again after that
	loginWait = 20 * time.Second
	loginMark = "__errand_login__"
)

var login struct {
	sync.Mutex
	environ []string
	at      time.Time
}

// loginEnviron: the variables of a login shell; this agent's own where there is no such thing
func loginEnviron() []string {
	login.Lock()
	defer login.Unlock()
	if login.environ == nil || time.Since(login.at) > loginKept {
		if got := readLogin(); got != nil {
			login.environ = got
		} else if login.environ == nil {
			login.environ = os.Environ()
		}
		login.at = time.Now()
	}
	return append([]string{}, login.environ...)
}

// systemPath: the PATH a session begins with
func systemPath() string {
	if data, err := os.ReadFile("/etc/environment"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PATH="); ok && v != "" {
				return strings.Trim(v, `"'`)
			}
		}
	}
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

func readLogin() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	env := []string{"PATH=" + systemPath(), "SHELL=" + shell}
	home, _ := os.UserHomeDir()
	env = append(env, "HOME="+home)
	if u, err := user.Current(); err == nil {
		env = append(env, "USER="+u.Username, "LOGNAME="+u.Username)
	}
	for _, k := range []string{"LANG", "LC_ALL", "TMPDIR", "XDG_RUNTIME_DIR", "ERRAND_CACHE_DIR", "ERRAND_SHARED"} { // the session's, and where errand is
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lic", "printf '\\n"+loginMark+"\\n'; env -0")
	cmd.Env, cmd.Dir, cmd.SysProcAttr, cmd.WaitDelay = env, home, system.Apart(), time.Second
	cmd.Cancel = func() error { return system.SignalGroup(cmd.Process.Pid, 9) }
	out, err := cmd.Output()
	_, said, ok := bytes.Cut(out, []byte(loginMark+"\n"))
	if err != nil && !ok {
		return nil
	}
	var got []string
	for _, kv := range strings.Split(string(said), "\x00") {
		k, _, isVar := strings.Cut(kv, "=")
		if !isVar || k == "PWD" || k == "OLDPWD" || k == "SHLVL" || k == "_" {
			continue
		}
		got = append(got, kv)
	}
	if p, _ := kit.EnvOf(got, "PATH"); p == "" || !filepath.IsAbs(home) {
		return nil
	}
	return got
}
