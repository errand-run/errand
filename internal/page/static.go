// The views a run opens as they are ( `errand.Static`, ADR 0017 §5 ): a directory carrying its own page, served from its
// copy here, on a server of its own beside the page.
//
//	URL     http://127.0.0.1:<port>/<token>/<id>/     the token in the path, kept for its user alone
//	what    only the directories registered, files read-only, never above their directory
//	.errand/  reserved in every view: the services its `services.json` asks for, and a relay to them ( services.go )
//
// The page's own preview is a sandboxed frame, which cannot read the files beside a page: on this origin, a run's page
// needs no cookie and reaches nothing of errand.

package page

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/svc"
)

// viewID: a view by its run and its path in it: the same for whoever computes it
func viewID(leaf, rel string) string {
	sum := sha1.Sum([]byte(runinfo.RunIdent(leaf) + "/" + strings.Trim(filepath.ToSlash(rel), "/")))
	return hex.EncodeToString(sum[:])[:16]
}

func staticToken() string {
	return keptSecret(filepath.Join(kit.LocalCache(), "page", "static-token"), 24)
}

func staticPort() int {
	var s struct {
		Static int `json:"static"`
		Pub    int `json:"public_static"`
	}
	if data, err := os.ReadFile(filepath.Join(kit.LocalCache(), "page", "said.json")); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Pub > 0 {
		return s.Pub
	}
	return s.Static
}

func staticURL(vid, index string) string {
	if index == "index.html" {
		index = ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/%s/%s/%s", staticPort(), staticToken(), vid, index)
}

// servedAt: where a view opened as it is is served here, and whether it is ready: its index is there, and, a store, its
// first frame
func servedAt(here bool, leaf, p, index string) (string, bool) {
	if staticPort() == 0 {
		return "", false
	}
	rel, inside := strings.CutPrefix(p, leaf+"/")
	vid := viewID(leaf, rel)
	if !inside {
		vid = viewID(p, "")
	}
	dir, known := svc.RegisteredViews()[vid]
	if known { // ( of a directory that is there )
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			known = false
		}
	}
	if !known {
		if st, err := os.Stat(p); !here || err != nil || !st.IsDir() {
			return "", false
		}
		svc.RegisterView(vid, p)
		dir = p
	}
	_, err := os.Stat(filepath.Join(dir, index))
	ready := err == nil
	if frames, err := os.ReadDir(filepath.Join(dir, "frames")); err == nil && len(frames) == 0 {
		ready = false
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", staticPort()), 300*time.Millisecond); err != nil {
		ready = false
	} else {
		c.Close()
	}
	return staticURL(vid, index), ready
}

func staticServe(rw http.ResponseWriter, r *http.Request, services func(rw http.ResponseWriter, r *http.Request, dir, vid string, rest []string)) {
	if !loopback(r) {
		http.Error(rw, "not for this address", http.StatusForbidden)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if subtle.ConstantTimeCompare([]byte(parts[0]), []byte(staticToken())) != 1 {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}
	if len(parts) < 2 {
		http.Error(rw, "no such view here", http.StatusNotFound)
		return
	}
	dir, ok := svc.RegisteredViews()[parts[1]]
	if !ok {
		http.Error(rw, "no such view here", http.StatusNotFound)
		return
	}
	if len(parts) >= 3 && parts[2] == ".errand" { // what the page asks errand for
		services(rw, r, dir, parts[1], parts[3:])
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(rw, "files are read only", http.StatusMethodNotAllowed)
		return
	}
	if len(parts) == 2 { // ( the files are read relative to the page )
		http.Redirect(rw, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return
	}
	rel, err := url.PathUnescape(strings.Join(parts[2:], "/"))
	if err != nil {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}
	if rel == "" {
		rel = "index.html"
	}
	real, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(rel)))
	root, rerr := filepath.EvalSymlinks(dir)
	if err != nil || rerr != nil || !strings.HasPrefix(real, root+string(filepath.Separator)) {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(real)
	if err != nil {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	http.ServeContent(rw, r, filepath.Base(real), st.ModTime(), f)
}

func loopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return host == "127.0.0.1" || host == "localhost"
}

func secret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// keptSecret: a secret of this user's, the same from one start to the next
func keptSecret(path string, n int) string {
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return strings.TrimSpace(string(data))
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	_ = os.WriteFile(tmp, []byte(secret(n)), 0o600)
	if os.Link(tmp, path) != nil { // the first one written wins: two at once agree
		if _, err := os.Stat(path); err != nil {
			_ = os.Rename(tmp, path)
		}
	}
	_ = os.Remove(tmp)
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
