// Finding this machine's agent ( ADR 0035 ): where the agents of a machine are, which live, which is ahead of this
// program, and the socket of the one this program talks to -- started first when it is not there.

package reach

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/system"
)

// Release: what a command line or another agent may ask of an agent. Raised when that changes; one of a lower one is told
// to update rather than answered by halves. ( A variable, so that a test can build an older errand. )
var Release = "1"

var buildMark string // ( a test's: the same program, as another version of itself )

const KeepDflt = 900.0 // seconds an agent stays with nothing asked and nothing running

var SelfHash string

type AgentVersion struct {
	Kind    string  `json:"kind,omitempty"` // "" the machine's; "container"
	Release string  `json:"release"`
	Errand  string  `json:"errand"`
	Pid     int     `json:"pid"`
	Started float64 `json:"started"`
}

func ReleaseOf(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func PlaceRoot() string { return filepath.Join(kit.LocalCache(), "place", queue.Host()) }

// PlaceDir: where the agent of this program is, on this machine
func PlaceDir() string { return filepath.Join(PlaceRoot(), OwnHash()) }

// OwnHash: what names this program: its contents. Kept beside its size and its date: a command does not read ten
// megabytes to find its agent.
func OwnHash() string {
	if SelfHash != "" {
		return SelfHash
	}
	self := kit.SelfPath()
	if isOwnCopy(self) { // named by what it is
		SelfHash = filepath.Base(filepath.Dir(self))
		return SelfHash
	}
	st, err := os.Stat(self)
	if err != nil {
		SelfHash = "unknown"
		return SelfHash
	}
	sum := sha256.Sum256([]byte(self))
	kept := filepath.Join(kit.LocalCache(), "bin", ".known-"+hex.EncodeToString(sum[:])[:16])
	mark := fmt.Sprintf("%d %d ", st.Size(), st.ModTime().UnixNano())
	if data, err := os.ReadFile(kept); err == nil && strings.HasPrefix(string(data), mark) && len(data) == len(mark)+16 {
		SelfHash = string(data[len(mark):])
		return SelfHash
	}
	if SelfHash, _ = kit.HashOfFile(self); SelfHash == "" {
		SelfHash = "unknown"
		return SelfHash
	}
	if os.MkdirAll(filepath.Dir(kept), 0o755) == nil {
		_ = kit.WriteWhole(kept, mark+SelfHash)
	}
	return SelfHash
}

// typedVar: the program that was typed, said to the agent it starts: what a distribution carries ( the programs of other
// systems, errand's Python ) is beside it, and the agent runs from a copy
const typedVar = system.Typed

// TypedPath: this program as it is installed -- itself, when it is no copy errand made
func TypedPath(getenv func(string) string) string {
	if self := kit.SelfPath(); !isOwnCopy(self) {
		return self
	}
	return getenv(typedVar)
}

// isOwnCopy: whether a program is one errand put in its own directory, named by what it is
func isOwnCopy(self string) bool {
	dir := filepath.Dir(self)
	return len(filepath.Base(dir)) == 16 && filepath.Dir(dir) == filepath.Join(kit.LocalCache(), "bin")
}

// WithTyped: surroundings that say which program was typed
func WithTyped(env []string) []string {
	return append(append([]string{}, env...), typedVar+"="+TypedPath(os.Getenv))
}

// OwnCopy: this program, in errand's own directory -- what a daemon runs from. The file that was installed may be
// replaced at any time ( an update ): what runs goes on as it is, starts what it needs of itself, and is named by what it
// is; and nothing holds the file an update replaces, which Windows would not let go.
func OwnCopy() string {
	self, hash := kit.SelfPath(), OwnHash()
	if hash == "unknown" {
		return self
	}
	name := "errand"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := filepath.Join(kit.LocalCache(), "bin", hash)
	path := filepath.Join(dir, name)
	if self == path {
		return self
	}
	now := time.Now()
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		_ = os.Chtimes(path, now, now) // ( it is used: agent/rounds.go )
		return path
	}
	if os.MkdirAll(dir, 0o755) != nil {
		return self
	}
	from, err := os.Open(self)
	if err != nil {
		return self
	}
	defer from.Close()
	part, err := os.CreateTemp(dir, ".errand-*")
	if err != nil {
		return self
	}
	_, err = io.Copy(part, from)
	if cerr := part.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(part.Name(), 0o755)
	}
	if err == nil {
		if got, _ := kit.HashOfFile(part.Name()); got != hash { // ( it was replaced while it was read )
			err = errors.New("changed")
		} else {
			err = os.Rename(part.Name(), path)
		}
	}
	if err != nil {
		_ = os.Remove(part.Name())
		if kit.Exists(path) {
			return path
		}
		return self
	}
	return path
}

// AgentAlive: whether an agent holds that directory -- its lock says, held for its whole life
func AgentAlive(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if system.TryLock(f) == nil {
		_ = system.Unlock(f)
		return false
	}
	return true
}

// Others: the directories of this machine's other agents that live
func Others(root, mine string) []string {
	var out []string
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if e.IsDir() && dir != mine && AgentAlive(dir) {
			out = append(out, dir)
		}
	}
	return out
}

// Machines: among those, the agents of the machine itself ( of other versions ) -- not the ones inside containers
func Machines(root, mine string) []string {
	var out []string
	for _, dir := range Others(root, mine) {
		if VersionOf(dir).Kind == "" {
			out = append(out, dir)
		}
	}
	return out
}

func VersionOf(dir string) AgentVersion {
	var v AgentVersion
	data, _ := os.ReadFile(filepath.Join(dir, "version"))
	_ = json.Unmarshal(data, &v)
	return v
}

// NewerHere: the release of an agent of this machine that is ahead of this program; "" when none is
func NewerHere() string {
	for _, dir := range Machines(PlaceRoot(), PlaceDir()) {
		if v := VersionOf(dir); ReleaseOf(v.Release) > ReleaseOf(Release) {
			return v.Release
		}
	}
	return ""
}

// AwaitHello: a link's stream, found past whatever was printed before it
func AwaitHello(r io.Reader) error {
	hello := []byte(linkHello)
	seen, one := 0, make([]byte, 1)
	for read := 0; seen < len(hello); read++ {
		if read > 1<<20 {
			return errors.New("what answered is not errand")
		}
		if _, err := io.ReadFull(r, one); err != nil {
			return err
		}
		if one[0] == hello[seen] {
			seen++
		} else if seen = 0; one[0] == hello[0] {
			seen = 1
		}
	}
	return nil
}

// ReachDaemon: the socket of a daemon of this program on this machine, in `dir` -- the daemon started first, by
// the verb that is it, when it is not there. Nobody's child: it outlives whoever started it.
func ReachDaemon(dir, verb string) (net.Conn, error) {
	if conn, err := kit.DialDir(dir, 200*time.Millisecond); err == nil {
		return conn, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	log, _ := os.OpenFile(filepath.Join(dir, "log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	cmd := exec.Command(OwnCopy(), verb, "--dir", dir)
	cmd.Env = WithTyped(os.Environ())
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = system.Detached()
	err := cmd.Start()
	if err != nil && system.Lent() != nil { // ( started from a job that does not let its processes leave it )
		again := exec.Command(cmd.Path, cmd.Args[1:]...)
		again.Env = cmd.Env
		again.Stdout, again.Stderr, again.SysProcAttr = log, log, system.Lent()
		cmd, err = again, again.Start()
	}
	if err != nil {
		return nil, fmt.Errorf("errand's %s could not be started on this machine: %w", verb, err)
	}
	_ = cmd.Process.Release()
	if log != nil {
		log.Close()
	}
	var conn net.Conn
	for until := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) { // ( until it listens )
		if conn, err = kit.DialDir(dir, time.Second); err == nil || time.Now().After(until) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("errand's %s was started on this machine and does not answer ( its words: %s ): %w", verb, filepath.Join(dir, "log"), err)
	}
	return conn, nil
}

// ReachAgent: the socket of this program's agent on this machine
func ReachAgent() (net.Conn, error) { return ReachDaemon(PlaceDir(), "place") }
