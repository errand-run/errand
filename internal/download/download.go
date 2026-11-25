// A file by its hash, in a cache errand declares itself ( ADR 0013 ), and the request that waits ( ADR 0035 ).
//
//	"$ERRAND" download URL [--sha256 HASH] [--name NAME]      prints the file's path
//
//	<root>/errand-cache/downloads/<sha256>/<its name>          the file: a name a tool can read, under what it holds
//	<root>/errand-cache/downloads/by-url/<hash of the URL>.json    which hash a URL gave, when none was said
//	<root>/errand-cache/downloads/.errand-work/                what is under way: parts, locks, and
//	    wanted/<id>.json                                       a request left by a run that cannot reach its URL
//	    answers/<id>/answer.json                               its answer, where the run waits for it
//
// **A run with no way to its URL leaves a request in the directory, and waits.** No link is kept for it: the request
// is a file, on a disk whoever can answer sees. It is answered by the first that has a way --
// the agent that holds the run, the agent that one depends on, whoever follows the run from another machine ( which
// downloads it there, and sends it into the copy ). With nobody there, it waits: it is not lost.

package download

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
)

const (
	downloadsZone2 = "errand-cache/downloads"
	workDir        = ".errand-work" // excluded from every sync
	askedFor       = 24 * time.Hour // how long a run waits for somebody with a way to its URL
)

// Want is a request a run left: a file it needs, and where it waits for the answer.
type Want struct {
	V      int     `json:"v"`
	ID     string  `json:"id"`
	URL    string  `json:"url"`
	SHA256 string  `json:"sha256,omitempty"`
	Name   string  `json:"name"`
	Dir    string  `json:"dir"`    // the downloads' directory, on the disk the run sees
	Answer string  `json:"answer"` // where the run waits for it
	Host   string  `json:"host"`
	Since  float64 `json:"since"`
}

type wantAnswer struct {
	SHA256 string `json:"sha256,omitempty"`
	Error  string `json:"error,omitempty"`
}

// offlineError: there is no way from here to that URL -- as opposed to a way that leads to a refusal
type offlineError struct{ why string }

func (e offlineError) Error() string { return e.why }

func downloadsOf(root string) string { return filepath.Join(root, filepath.FromSlash(downloadsZone2)) }

// nameOf: the file's name, from the URL's path: what a tool that reads the extension needs
func NameFromURL(raw string) string {
	u, err := url.Parse(raw)
	path := raw
	if err == nil {
		path = u.Path
	}
	path = strings.TrimRight(path, "/")
	name := path[strings.LastIndex(path, "/")+1:]
	if un, err := url.PathUnescape(name); err == nil {
		name = un
	}
	var b strings.Builder
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-+", c) || c > 127 {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	if name = strings.TrimLeft(b.String(), "."); name == "" {
		return "download"
	}
	return name
}

func urlKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:32]
}

// haveDownload: the file, if it is already here -- by its hash when given, else through the URL's index
func haveDownload(d, raw, sha, name string) string {
	if sha == "" {
		var got struct{ SHA256, Name string }
		data, err := os.ReadFile(filepath.Join(d, "by-url", urlKey(raw)+".json"))
		if err != nil || kit.JsonUnmarshal(data, &got) != nil || got.SHA256 == "" {
			return ""
		}
		sha = got.SHA256
		if got.Name != "" {
			name = got.Name
		}
	}
	p := filepath.Join(d, sha, name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func rememberDownload(d, raw, sha, name string) {
	_ = os.MkdirAll(filepath.Join(d, "by-url"), 0o755)
	_ = kit.WriteRecord(filepath.Join(d, "by-url", urlKey(raw)+".json"), map[string]string{"url": raw, "sha256": sha, "name": name})
}

// surroundingsOf: a variable in the surroundings a download is made in -- those of whoever asked ( its proxy, what
// it says of its way out ), this process's when nobody said
func surroundingsOf(environ []string, name string) string {
	if environ == nil {
		return os.Getenv(name)
	}
	out := ""
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			out = v
		}
	}
	return out
}

// proxyOf: the proxy those surroundings name for a request, as `curl` and `pip` read it
func proxyOf(environ []string) func(*http.Request) (*url.URL, error) {
	if environ == nil {
		return http.ProxyFromEnvironment
	}
	return func(req *http.Request) (*url.URL, error) {
		host := req.URL.Hostname()
		for _, no := range strings.Split(surroundingsOf(environ, "NO_PROXY")+","+surroundingsOf(environ, "no_proxy"), ",") {
			if no = strings.TrimSpace(strings.TrimPrefix(no, ".")); no != "" && (no == "*" || host == no || strings.HasSuffix(host, "."+no)) {
				return nil, nil
			}
		}
		for _, name := range []string{req.URL.Scheme + "_proxy", strings.ToUpper(req.URL.Scheme) + "_PROXY", "ALL_PROXY", "all_proxy"} {
			if p := surroundingsOf(environ, name); p != "" {
				if !strings.Contains(p, "://") {
					p = "http://" + p
				}
				return url.Parse(p)
			}
		}
		return nil, nil
	}
}

// FetchURL: `raw` into `d/<its hash>/name`, checked against `sha` when given -> its hash. An offlineError when
// there is no way to it; any other error for a way that led to a refusal, or contents that are not those asked.
func FetchURL(raw, d, name, sha string, environ []string, progress func(done, total int64)) (string, error) {
	if surroundingsOf(environ, "ERRAND_NO_INTERNET") != "" && !strings.HasPrefix(raw, "file:") {
		return "", offlineError{raw + ": this place is said to have no way out ( ERRAND_NO_INTERNET )"}
	}
	parts := filepath.Join(d, workDir, "parts")
	if err := os.MkdirAll(parts, 0o755); err != nil {
		return "", err
	}
	var body io.ReadCloser
	var total int64
	if path, ok := strings.CutPrefix(raw, "file://"); ok {
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("%s: %v", raw, err)
		}
		if st, err := f.Stat(); err == nil {
			total = st.Size()
		}
		body = f
	} else {
		req, err := http.NewRequest("GET", raw, nil)
		if err != nil {
			return "", fmt.Errorf("%s: %v", raw, err)
		}
		req.Header.Set("User-Agent", "errand")
		client := &http.Client{Transport: &http.Transport{Proxy: proxyOf(environ),
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second}}
		resp, err := client.Do(req)
		if err != nil { // never got an answer: no way from here
			var said *url.Error
			if errors.As(err, &said) {
				err = said.Err
			}
			return "", offlineError{fmt.Sprintf("%s: %v", raw, err)}
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			resp.Body.Close()
			return "", fmt.Errorf("%s: %s", raw, resp.Status)
		}
		body, total = resp.Body, resp.ContentLength
	}
	defer body.Close()
	part := filepath.Join(parts, fmt.Sprintf("%s.%d.%d", name, os.Getpid(), time.Now().UnixNano()))
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	defer os.Remove(part)
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var size int64
	said := time.Time{}
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return "", werr
			}
			h.Write(buf[:n])
			size += int64(n)
			if progress != nil && time.Since(said) > 500*time.Millisecond {
				said = time.Now()
				progress(size, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return "", fmt.Errorf("%s: cut after %d bytes ( %v )", raw, size, err)
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if sha != "" && digest != strings.ToLower(sha) {
		return "", fmt.Errorf("%s: its sha256 is %s, not %s as asked -- removed", raw, digest, sha)
	}
	if err := os.MkdirAll(filepath.Join(d, digest), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(part, filepath.Join(d, digest, name)); err != nil {
		return "", err
	}
	return digest, nil
}

// lockDownload: one fetch of a URL at a time in a directory -- a directory made by `mkdir`, atomic on NFS and Lustre.
// One left by a run that died is taken over after an hour.
func lockDownload(d, raw string) (func(), error) {
	dir := filepath.Join(d, workDir, "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, urlKey(raw))
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			return func() { _ = os.Remove(lock) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if st, serr := os.Stat(lock); serr == nil && time.Since(st.ModTime()) > time.Hour {
			_ = os.Remove(lock)
			continue
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// AnswerWant: what a run wants, fetched into its directory by this process, and the answer written where it waits.
// -> whether it was answered ( a file, or a refusal ): not when there is no way from here either
func AnswerWant(w Want) (wantAnswer, bool) {
	sha := ""
	if found := haveDownload(w.Dir, w.URL, w.SHA256, w.Name); found != "" {
		sha = filepath.Base(filepath.Dir(found))
	} else {
		var err error
		if sha, err = FetchURL(w.URL, w.Dir, w.Name, w.SHA256, nil, nil); err != nil {
			var off offlineError
			if errors.As(err, &off) {
				return wantAnswer{Error: err.Error()}, false
			}
			a := wantAnswer{Error: err.Error()}
			sayAnswer(w, a)
			return a, true
		}
	}
	a := wantAnswer{SHA256: sha}
	sayAnswer(w, a)
	return a, true
}

func sayAnswer(w Want, a wantAnswer) {
	_ = os.MkdirAll(filepath.Dir(w.Answer), 0o755)
	_ = kit.WriteRecord(w.Answer, a)
}

// WantsIn: the requests left in a project's downloads, not answered yet
func WantsIn(root string) []Want {
	var out []Want
	files, _ := filepath.Glob(filepath.Join(downloadsOf(root), workDir, "wanted", "*.json"))
	for _, f := range files {
		var w Want
		if data, err := os.ReadFile(f); err == nil && kit.JsonUnmarshal(data, &w) == nil && w.ID != "" {
			if _, answered := os.Stat(w.Answer); answered != nil {
				out = append(out, w)
			}
		}
	}
	return out
}

func DownloadMain(args []string) int {
	bad := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "errand download: "+format+"\n", a...)
		return 1
	}
	raw, sha, name, root := "", "", "", os.Getenv("ERRAND_ROOT")
	for i := 0; i < len(args); i++ {
		value := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch a := args[i]; {
		case a == "--sha256":
			sha = strings.ToLower(value())
		case a == "--name":
			name = value()
		case a == "--root":
			root = value()
		case strings.HasPrefix(a, "-"):
			return bad("%s: not an option of it. `errand download URL [--sha256 HASH] [--name NAME]`", a)
		default:
			raw = a
		}
	}
	if raw == "" {
		return bad("`errand download URL [--sha256 HASH] [--name NAME]`: prints the file's path")
	}
	if root == "" {
		cwd, _ := os.Getwd()
		root, _ = project.FindRoot(cwd)
	}
	if name == "" {
		name = NameFromURL(raw)
	}
	d := downloadsOf(root)
	if found := haveDownload(d, raw, sha, name); found != "" {
		fmt.Println(found)
		return 0
	}
	unlock, err := lockDownload(d, raw)
	if err != nil {
		return bad("%v", err)
	}
	defer unlock()
	if found := haveDownload(d, raw, sha, name); found != "" { // fetched by another run meanwhile
		fmt.Println(found)
		return 0
	}
	step := func(text string, done float64) {
		if path := os.Getenv("ERRAND_STEPS"); path != "" {
			line := map[string]any{"t": kit.Seconds(time.Now()), "text": text}
			if done >= 0 {
				line["done"] = done
			}
			_ = kit.AppendLine(path, line)
		}
	}
	digest, err := FetchURL(raw, d, name, sha, nil, func(size, total int64) {
		text, done := fmt.Sprintf("download %s: %.1f MB", name, float64(size)/1e6), -1.0
		if total > 0 {
			text += fmt.Sprintf(" of %.1f", float64(total)/1e6)
			done = float64(size) / float64(total)
		}
		step(text, done)
	})
	var off offlineError
	if errors.As(err, &off) {
		if os.Getenv("ERRAND_PROTOCOL") == "" { // not a run of errand's: nobody looks at this directory for it
			return bad("%v. No way from here, and nobody to ask: download it where there is one ( `errand download %s` ), and the next push brings it", err, raw)
		}
		digest, err = askFor(d, raw, sha, name, step)
	}
	if err != nil {
		return bad("%v", err)
	}
	rememberDownload(d, raw, digest, name)
	fmt.Println(filepath.Join(d, digest, name))
	return 0
}

// askFor: a request left in the directory, and its answer waited for -> the file's hash
func askFor(d, raw, sha, name string, step func(string, float64)) (string, error) {
	id := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	w := Want{V: 1, ID: id, URL: raw, SHA256: sha, Name: name, Dir: d, Answer: filepath.Join(d, workDir, "answers", id, "answer.json"),
		Host: queue.Host(), Since: kit.Seconds(time.Now())}
	asked := filepath.Join(d, workDir, "wanted", id+".json")
	if err := os.MkdirAll(filepath.Dir(asked), 0o755); err != nil {
		return "", err
	}
	if err := kit.WriteRecord(asked, w); err != nil {
		return "", err
	}
	defer os.Remove(asked)
	defer os.RemoveAll(filepath.Dir(w.Answer))
	step("download "+name+": no way from here -- waiting for somebody who has one", -1)
	for until := time.Now().Add(askedFor); time.Now().Before(until); time.Sleep(300 * time.Millisecond) {
		var a wantAnswer
		data, err := os.ReadFile(w.Answer)
		if err != nil || kit.JsonUnmarshal(data, &a) != nil {
			continue
		}
		if a.Error != "" {
			return "", errors.New(a.Error)
		}
		if _, err := os.Stat(filepath.Join(d, a.SHA256, name)); err != nil {
			return "", fmt.Errorf("%s: answered, but %s is not there", raw, filepath.Join(d, a.SHA256, name))
		}
		return a.SHA256, nil
	}
	return "", fmt.Errorf("%s: nobody with a way to it came in %s", raw, askedFor)
}
