// What the command line and the agent both stand on ( ADR 0035 ): errand's frames, where its files and its sockets
// are, and a few small things. The command line is this, a link and what draws -- and nothing of what does the work.

package kit

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/system"
)

const (
	OPEN = iota
	REPLY
	OUT
	ERR
	IN
	EOF
	SIGNAL
	CLOSE
	EXIT
	RESIZE
	NOTE // from the far side of a link ( reach/there.go ): something to say about a run that is not its output
)

const (
	headerSize = 9
	maxFrame   = 1 << 24
	Chunk      = 65536
	FlushFor   = 5 * time.Second
)

var Grace = [2]time.Duration{2 * time.Second, 5 * time.Second}

func Num(v any, dflt float64) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return dflt
}

func JsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func WriteWhole(path, text string) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Started: when pid started, as a token that differs for a pid reused:
// field 22 of /proc/<pid>/stat on Linux, `ps -o lstart=` elsewhere.
func Started(pid int) any {
	if s, ok := system.Started(pid); ok {
		return s
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(data)
		if i := strings.LastIndex(s, ")"); i >= 0 {
			f := strings.Fields(s[i+1:])
			if len(f) > 19 {
				return f[19]
			}
		}
		return nil
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil
	}
	return strings.TrimSpace(string(out))
}

func Frame(channel uint32, kind byte, payload []byte) []byte {
	b := make([]byte, headerSize+len(payload))
	binary.BigEndian.PutUint32(b[0:4], channel)
	b[4] = kind
	binary.BigEndian.PutUint32(b[5:9], uint32(len(payload)))
	copy(b[headerSize:], payload)
	return b
}

func FrameJSON(channel uint32, kind byte, v any) []byte {
	p, _ := json.Marshal(v)
	return Frame(channel, kind, p)
}

func ReadFrame(r io.Reader) (uint32, byte, []byte, error) {
	head := make([]byte, headerSize)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, 0, nil, err
	}
	channel, kind, length := binary.BigEndian.Uint32(head[0:4]), head[4], binary.BigEndian.Uint32(head[5:9])
	if length > maxFrame || kind < OPEN || kind > NOTE {
		return 0, 0, nil, fmt.Errorf("not a frame ( type %d, length %d )", kind, length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, 0, nil, err
	}
	return channel, kind, payload, nil
}

// SockAddress: the path a unix socket at `path` is bound and reached by -- itself, or the same file through a short
// link to its directory when the path is too long ( agents/endpoint.address ).
func SockAddress(path string) (string, error) {
	if len(path) < 100 {
		return path, nil
	}
	base := filepath.Join(os.TempDir(), filepath.Base(ShortDir()))
	if st, err := os.Stat("/tmp"); err == nil && st.IsDir() {
		base = ShortDir()
	}
	if err := os.Mkdir(base, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	st, err := os.Lstat(base)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not this user's directory", base)
	}
	if uid, ok := system.Owner(st); ok && uid != os.Getuid() {
		return "", fmt.Errorf("%s is not this user's directory", base)
	}
	target := filepath.Dir(path)
	sum := sha1.Sum([]byte(target))
	link := filepath.Join(base, hex.EncodeToString(sum[:])[:12])
	if now, err := os.Readlink(link); err != nil || now != target {
		tmp := filepath.Join(base, fmt.Sprintf(".%s.%d", filepath.Base(link), os.Getpid()))
		_ = os.Remove(tmp)
		if err := os.Symlink(target, tmp); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, link); err != nil {
			return "", err
		}
	}
	return filepath.Join(link, filepath.Base(path)), nil
}

func DialDir(dir string, timeout time.Duration) (net.Conn, error) {
	addr, err := SockAddress(filepath.Join(dir, "sock"))
	if err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", addr, timeout)
}

func LocalCache() string { return system.Cache() }

// ShortDir: a short directory of this user's and instance's, for what a long path cannot be ( sockets ): /tmp/errand-<uid>[-<instance>]
func ShortDir() string {
	if inst := system.Instance(); inst != "main" {
		return fmt.Sprintf("/tmp/errand-%d-%s", os.Getuid(), inst)
	}
	return fmt.Sprintf("/tmp/errand-%d", os.Getuid())
}

func ReadJSON(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func RandomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func Strs(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, len(raw))
	for i, a := range raw {
		out[i] = fmt.Sprint(a)
	}
	return out
}

func Text(v any) string {
	s, _ := v.(string)
	return s
}

func Seconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func WriteRecord(path string, v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	part := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".part")
	if err := os.WriteFile(part, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(part, path)
}

func HashOfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func SelfPath() string {
	self, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	return self
}

func IsDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
