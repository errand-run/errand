package layer

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/errand-run/errand/internal/project"
)

const vcvars = `C:\BuildTools\VC\Auxiliary\Build\vcvars64.bat`

var percent = regexp.MustCompile(`%[^%]+%`)

var (
	toolsMu   sync.Mutex
	toolsKept map[string]string
)

// windowsTools: what the commands of a Windows are given there -- the build tools' variables ( what `vcvars64` sets, asked
// of it once ), the venv errand keeps on a machine that is somebody's
func windowsTools(l project.Layer, c Command, ctx Context) (Command, error) {
	if ready := l.String("ready"); ready != "" {
		if _, err := os.Stat(filepath.FromSlash(ready)); err != nil {
			return c, ErrDeclared("this Windows is not set up for this declaration: errand setup")
		}
	}
	env := map[string]string{}
	path := ctx.getenv("PATH")
	if path == "" {
		path = ctx.getenv("Path")
	}
	if msvc, _ := l["msvc"].(bool); msvc {
		vars, err := msvcVars()
		if err != nil {
			return c, err
		}
		for k, v := range vars {
			if strings.EqualFold(k, "PATH") {
				path = v
			} else {
				env[k] = v
			}
		}
	}
	if box, _ := l["box"].(bool); box {
		path = `C:\errand\venv\Scripts;` + path
	}
	env["PATH"] = path
	// what the layers further in say, as cmd.exe's `set` would leave it: `%PATH%` and the like read
	for k, v := range c.Env {
		v = percent.ReplaceAllStringFunc(v, func(name string) string {
			for have, value := range env {
				if strings.EqualFold(have, name[1:len(name)-1]) {
					return value
				}
			}
			if value := ctx.getenv(name[1 : len(name)-1]); value != "" {
				return value
			}
			return name
		})
		if strings.EqualFold(k, "PATH") {
			k = "PATH"
		}
		env[k] = v
	}
	return Command{c.Argv, env}, nil
}

// msvcVars: what `vcvars64` changes of the surroundings
func msvcVars() (map[string]string, error) {
	toolsMu.Lock()
	defer toolsMu.Unlock()
	if toolsKept != nil {
		return toolsKept, nil
	}
	file, err := os.CreateTemp("", "errand-vcvars-*.bat") // ( a file: cmd.exe's quotes are not another program's )
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	_, _ = file.WriteString("@call \"" + vcvars + "\" >nul 2>&1\r\n@if errorlevel 1 exit /b 1\r\n@set\r\n")
	file.Close()
	out, err := exec.Command("cmd.exe", "/d", "/c", file.Name()).Output()
	if err != nil {
		return nil, ErrDeclared("the build tools are not on this Windows ( " + vcvars + " ): errand setup, or msvc = False")
	}
	was := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			was[strings.ToUpper(k)] = v
		}
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if ok && k != "" && was[strings.ToUpper(k)] != v {
			got[k] = v
		}
	}
	toolsKept = got
	return got, nil
}
