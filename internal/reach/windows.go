// Reaching a Windows: the same link, through its OpenSSH, whose shell is cmd.exe. What is sent there is `errand.exe`, by
// `scp`; it keeps its files under `C:\errand`.

package reach

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/system"
)

const WindowsCache = "C:/errand/cache"

func Backslashed(p string) string { return strings.ReplaceAll(p, "/", `\`) }

// scpArgv: ssh's options as scp reads them -- its port is `-P`
func (h HostSpec) scpArgv() []string {
	prog := h.getenv("ERRAND_SCP")
	if prog == "" {
		prog = "scp"
	}
	out := []string{prog, "-q"}
	for i := 0; i < len(h.Options); i++ {
		if h.Options[i] == "-p" {
			out = append(out, "-P")
		} else {
			out = append(out, h.Options[i])
		}
	}
	return out
}

// Talk: a cmd.exe line over there -> what it prints, and how it ended
func (h HostSpec) Talk(line string, wait time.Duration) (string, string, int, error) {
	cmd := h.command(append(h.argv(), line))
	var out, said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &said
	if err := cmd.Start(); err != nil {
		return "", "", 255, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-h.Stop:
		_ = cmd.Process.Kill()
		<-done
		return out.String(), said.String(), 255, errors.New("stopped")
	case <-time.After(wait):
		_ = cmd.Process.Kill()
		<-done
		return out.String(), said.String(), 255, fmt.Errorf("%s: nothing came back for %s", h.Host, Span(wait.Seconds()))
	}
	return out.String(), said.String(), cmd.ProcessState.ExitCode(), nil
}

// Must: a line that has to work
func (h HostSpec) Must(line, what string, wait time.Duration) (string, error) {
	out, said, code, err := h.Talk(line, wait)
	if err != nil {
		return out, err
	}
	if code != 0 {
		if why := h.whyNot(code, said); why != "" && code == 255 {
			return out, fmt.Errorf("%s", why)
		}
		return out, fmt.Errorf("%s failed on %s: %s", what, h.Host, kit.EndOf(said+out, 600))
	}
	return out, nil
}

func (h HostSpec) probeWindows() (*farInfo, error) {
	out, said, code, err := h.Talk("echo "+strings.TrimSuffix(probeMark, "|")+"=%PROCESSOR_ARCHITECTURE%=%USERPROFILE%=", 120*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %v", h.Host, err)
	}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), strings.TrimSuffix(probeMark, "|")+"=")
		parts := strings.Split(rest, "=")
		if !ok || len(parts) < 2 {
			continue
		}
		if strings.Contains(parts[0], "%") {
			return nil, fmt.Errorf("%s: its shell is not cmd.exe ( Windows' OpenSSH default ), or it is no Windows", h.Host)
		}
		i := &farInfo{V: 2, OS: "windows", Arch: strings.ToLower(parts[0]), Home: parts[1], Cache: WindowsCache}
		if h.Cache != "" {
			i.Cache = strings.TrimRight(strings.ReplaceAll(h.Cache, "\\", "/"), "/")
		}
		i.Cache += "/" + system.Instance()
		if !h.Booted { // ( a machine that is booted is asked each time: its name leads to another tomorrow )
			_ = os.MkdirAll(filepath.Dir(h.infoPath()), 0o755)
			_ = kit.WriteRecord(h.infoPath(), i)
		}
		return i, nil
	}
	if why := h.whyNot(code, said); why != "" {
		return nil, fmt.Errorf("%s", why)
	}
	return nil, fmt.Errorf("%s did not say what it is ( %s )", h.Host, kit.EndOf(said, 200))
}

func (h HostSpec) startWindows(program string) string {
	cache := baseOf(path.Dir(path.Dir(path.Dir(program)))) // <cache>/<instance>/bin/<hash>/errand
	return `set "ERRAND_CACHE_DIR=` + Backslashed(cache) + `" && set "ERRAND_INSTANCE=` + system.Instance() + `" && "` + Backslashed(program) + `.exe" there`
}

// shipWindows: the program sent -- written beside, then renamed
func (h HostSpec) shipWindows(local, dir string) error {
	b := Backslashed(dir)
	if _, err := h.Must(`if not exist "`+b+`" mkdir "`+b+`"`, "making "+b, time.Minute); err != nil {
		return err
	}
	part := fmt.Sprintf("errand-%d.part", os.Getpid())
	if err := h.Put(local, dir+"/"+part); err != nil {
		return err
	}
	_, err := h.Must(`cd /d "`+b+`" && ( if exist errand.exe ( del `+part+` ) else ( move /y `+part+` errand.exe >nul ) )`, "putting errand in place", time.Minute)
	return err
}

// Put: a file of this machine, there
func (h HostSpec) Put(local, remote string) error {
	argv := append(h.scpArgv(), local, h.Host+":"+remote)
	var cmd *exec.Cmd = h.command(argv)
	if said, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s could not be written on %s, as %s: %s", filepath.Base(local), h.Host, remote, kit.EndOf(string(said), 400))
	}
	return nil
}
