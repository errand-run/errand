package page

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/units"
)

func (w *webServer) serveStatic(rw http.ResponseWriter, r *http.Request) {
	defer w.inUse()()
	staticServe(rw, r, w.pageServices)
}

// inUse: the server is, from here to the call of what is returned
func (w *webServer) inUse() func() {
	count := func(by int) {
		w.mu.Lock()
		w.serving += by
		w.last = time.Now()
		w.mu.Unlock()
	}
	count(1)
	return func() { count(-1) }
}

// idle: for how long nobody has used it ( w.mu held )
func (w *webServer) idle() time.Duration {
	if w.serving > 0 {
		return 0
	}
	return time.Since(w.last)
}

// webKeep: the page's address, and the stores', kept by the system -- or given back
func webKeep(on bool) int {
	if !on {
		a, b := units.UnkeepPort(units.WebLabel()), units.UnkeepPort(units.StoresLabel())
		if a || b {
			fmt.Println("  given back")
		} else {
			fmt.Println("  the system kept nothing for the page")
		}
		return 0
	}
	if s, ok := runinfo.WebNow(); ok { // it holds the ports: it goes, and comes back behind them
		_ = stopPid(s.Pid)
	}
	self := reach.TypedPath(os.Getenv)
	for _, k := range []struct {
		label string
		port  int
		verb  string
	}{{units.WebLabel(), runinfo.PortOfUser(), "kept-page"}, {units.StoresLabel(), runinfo.PortOfUser() + 2, "kept-stores"}} {
		if err := units.KeepPort(k.label, []string{self, "web", "serve-" + k.verb}, "", k.port); err != nil {
			fmt.Fprintln(os.Stderr, "errand web keep:", err)
			return 1
		}
	}
	fmt.Printf("  http://127.0.0.1:%d/  kept by the system: the page starts at the first connection\n", runinfo.PortOfUser())
	return 0
}

func keptRelay(stores bool) int {
	l, err := units.InheritedListener()
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand web: started by hand? the system hands this its socket ( errand web keep )")
		return 2
	}
	units.RelayKept(l, 5*time.Minute, func() (net.Conn, error) {
		s, err := ensureWeb()
		if err != nil {
			return nil, err
		}
		port := s.Port
		if stores {
			port = s.Static
		}
		return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 30*time.Second)
	})
	return 0
}

// openDir: a directory opened as it is ( its index.html ), with the services it asks for
func openDir(dir string) int {
	abs, err := filepath.Abs(dir)
	if err == nil {
		if st, serr := os.Stat(filepath.Join(abs, "index.html")); serr != nil || st.IsDir() {
			err = errors.New(dir + ": no index.html in it")
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand open:", err)
		return 1
	}
	if _, err := ensureWeb(); err != nil {
		fmt.Fprintln(os.Stderr, "errand open:", err)
		return 1
	}
	vid := viewID(abs, "")
	svc.RegisterView(vid, abs)
	link := staticURL(vid, "index.html")
	openInBrowser(link)
	fmt.Println(link)
	return 0
}
