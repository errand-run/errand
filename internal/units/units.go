// Ports kept by the system: nothing of errand's runs between two uses ( ADR 0028 §6 ).
//
// macOS' `launchd` and Linux' `systemd --user` can listen on a port in a program's stead, and start the program at the
// first connection. errand hands them a port only when the user asks ( `errand web keep`, `errand serve forward … --keep` ).
// The program started is given the listening socket -- as its standard input under launchd ( `inetdCompatibility`, which
// waits ), as a passed descriptor under systemd --, serves what comes, and goes once idle; the system keeps the port.

package units

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
)

type keptUnit struct {
	Argv []string `json:"argv"`
	Port int      `json:"port"`
	Unit string   `json:"unit"`
	Made string   `json:"made"`
}

func keptFile() string { return filepath.Join(kit.LocalCache(), "kept.json") }

func keptUnits() map[string]keptUnit {
	out := map[string]keptUnit{}
	if data, err := os.ReadFile(keptFile()); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func KeptPort(label string) int { return keptUnits()[label].Port }

const (
	WebLabel    = "dev.errand.page"
	StoresLabel = "dev.errand.stores"
)

// KeptPorts: the page's and the stores', when the system keeps them
func KeptPorts() [2]int { return [2]int{KeptPort(WebLabel), KeptPort(StoresLabel)} }

func keepRefused() string {
	if os.Getenv("ERRAND_SHARED") == "1" || os.Getenv("SLURM_JOB_ID") != "" {
		return "this machine is shared with others: errand keeps nothing on it"
	}
	if _, err := exec.LookPath("sbatch"); err == nil && os.Getenv("ERRAND_SHARED") != "0" {
		return "this machine is a cluster's: errand keeps nothing on it"
	}
	switch runtime.GOOS {
	case "darwin":
		return ""
	case "linux":
		out, _ := exec.Command("systemctl", "--user", "is-system-running").Output()
		if strings.TrimSpace(string(out)) == "" {
			return "no `systemd --user` here"
		}
		return ""
	}
	return runtime.GOOS + ": no socket activation errand knows"
}

func keepEnviron() map[string]string {
	out := map[string]string{}
	for _, k := range []string{"PATH", "HOME", "ERRAND_CACHE_DIR", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "ERRAND_DECLARE_PYTHON", "PYTHONPATH"} {
		if v := os.Getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

func LaunchdPlist(label string, argv []string, port int, env map[string]string, log string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + esc(label) + `</string>
<key>AbandonProcessGroup</key><true/>
<key>inetdCompatibility</key><dict><key>Wait</key><true/></dict>
<key>ProgramArguments</key><array>`)
	for _, a := range argv {
		b.WriteString("<string>" + esc(a) + "</string>")
	}
	b.WriteString(`</array>
<key>Sockets</key><dict><key>Listeners</key><dict><key>SockNodeName</key><string>127.0.0.1</string><key>SockServiceName</key><string>` + strconv.Itoa(port) +
		`</string><key>SockType</key><string>stream</string><key>SockFamily</key><string>IPv4</string></dict></dict>
<key>EnvironmentVariables</key><dict>`)
	for k, v := range env {
		b.WriteString("<key>" + esc(k) + "</key><string>" + esc(v) + "</string>")
	}
	b.WriteString("</dict>\n<key>StandardErrorPath</key><string>" + esc(log) + "</string>\n</dict></plist>\n")
	return b.String()
}

func SystemdUnits(label string, argv []string, port int, env map[string]string) (string, string) {
	sock := fmt.Sprintf("[Unit]\nDescription=errand: %s\n\n[Socket]\nListenStream=127.0.0.1:%d\n\n[Install]\nWantedBy=sockets.target\n", label, port)
	var words []string
	for _, a := range argv {
		words = append(words, "'"+strings.ReplaceAll(a, "'", `\'`)+"'")
	}
	svc := "[Unit]\nDescription=errand: " + label + "\n\n[Service]\nExecStart=" + strings.Join(words, " ") + "\nKillMode=process\n"
	for k, v := range env {
		svc += "Environment=\"" + k + "=" + v + "\"\n"
	}
	return sock, svc
}

// KeepPort: the system listens on `port` for `argv` from now on, and after each login
func KeepPort(label string, argv []string, port int) error {
	if why := keepRefused(); why != "" {
		return errors.New(why)
	}
	dir := filepath.Join(kit.LocalCache(), "kept")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	where := ""
	if runtime.GOOS == "darwin" {
		where = filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		_ = os.MkdirAll(filepath.Dir(where), 0o755)
		if err := os.WriteFile(where, []byte(LaunchdPlist(label, argv, port, keepEnviron(), filepath.Join(dir, label+".log"))), 0o644); err != nil {
			return err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+label).Run() // ( an older one first )
		if out, err := exec.Command("launchctl", "bootstrap", domain, where).CombinedOutput(); err != nil {
			_ = os.Remove(where)
			return fmt.Errorf("launchctl bootstrap: %s", strings.TrimSpace(string(out)))
		}
	} else {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		units := filepath.Join(base, "systemd", "user")
		_ = os.MkdirAll(units, 0o755)
		sock, svc := SystemdUnits(label, argv, port, keepEnviron())
		where = filepath.Join(units, label+".socket")
		if err := os.WriteFile(where, []byte(sock), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(units, label+".service"), []byte(svc), 0o644); err != nil {
			return err
		}
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", label + ".socket"}} {
			if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
			}
		}
	}
	all := keptUnits()
	all[label] = keptUnit{argv, port, where, time.Now().Format("2006-01-02 15:04:05")}
	b, _ := json.MarshalIndent(all, "", " ")
	return kit.WriteWhole(keptFile(), string(b))
}

// UnkeepPort: the unit gone, the port free -> whether there was one
func UnkeepPort(label string) bool {
	all := keptUnits()
	_, was := all[label]
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
		_ = os.Remove(filepath.Join(home, "Library", "LaunchAgents", label+".plist"))
	} else {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		_ = exec.Command("systemctl", "--user", "disable", "--now", label+".socket").Run()
		_ = exec.Command("systemctl", "--user", "stop", label+".service").Run()
		for _, ext := range []string{".socket", ".service"} {
			_ = os.Remove(filepath.Join(base, "systemd", "user", label+ext))
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	delete(all, label)
	b, _ := json.MarshalIndent(all, "", " ")
	_ = kit.WriteWhole(keptFile(), string(b))
	return was
}

// InheritedListener: in a program the system started for its port, the listening socket
func InheritedListener() (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") != "" {
		return net.FileListener(os.NewFile(3, "listen"))
	}
	return net.FileListener(os.NewFile(0, "listen")) // launchd's, as its standard input
}

// RelayKept: each connection to a kept port, relayed to what `reach` says, until none came for a while
func RelayKept(l net.Listener, idle time.Duration, reach func() (net.Conn, error)) {
	var mu sync.Mutex
	active, last := 0, time.Now()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			active++
			last = time.Now()
			mu.Unlock()
			go func() {
				defer func() {
					c.Close()
					mu.Lock()
					active--
					last = time.Now()
					mu.Unlock()
				}()
				up, err := reach()
				if err != nil {
					body := "errand: " + err.Error() + "\n"
					fmt.Fprintf(c, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
					return
				}
				defer up.Close()
				var wg sync.WaitGroup
				wg.Add(2)
				pump := func(from, to net.Conn) {
					defer wg.Done()
					buf := make([]byte, 64<<10)
					for {
						n, err := from.Read(buf)
						if n > 0 {
							if _, err := to.Write(buf[:n]); err != nil {
								break
							}
						}
						if err != nil {
							break
						}
					}
					if t, ok := to.(*net.TCPConn); ok {
						_ = t.CloseWrite()
					}
				}
				go pump(c, up)
				go pump(up, c)
				wg.Wait()
			}()
		}
	}()
	for {
		time.Sleep(time.Second)
		mu.Lock()
		over := active == 0 && time.Since(last) > idle
		mu.Unlock()
		if over {
			return
		}
	}
}
