package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/units"
)

// ── the command ──────────────────────────────────────────────────────────────

func (o serveOptions) asked(t *reach.Term, req map[string]any) (map[string]any, error) {
	h, err := machineFor(t, o.host, o.env, o.root)
	if err != nil {
		return nil, err
	}
	l, err := reach.LinkTo(t, h)
	if err != nil {
		return nil, err
	}
	return l.Ask(map[string]any{"op": "services_do", "req": req})
}

// pageServicesMain: `errand serve look | fetch | install | probe`
func pageServicesMain(t *reach.Term, verb string, o serveOptions, words []string) int {
	fail := func(err error) int {
		t.Sayln("errand serve "+verb+":", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return 1
	}
	on := o.host
	if on == "" {
		on = "here"
	}
	switch verb {
	case "look":
		if len(words) != 1 {
			t.Sayln("errand serve look [--on M] DIR ( a store )")
			return 2
		}
		dir := words[0]
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(t.Cwd, dir)
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return fail(fmt.Errorf("%s: no such directory", words[0]))
		}
		req := svc.LookRequest(dir, "", on)
		paths, _ := req["paths"].(map[string][]string)
		for _, p := range o.paths {
			tool, at, _ := strings.Cut(p, "=")
			paths[tool] = append(paths[tool], at)
		}
		got, err := o.asked(t, req)
		if err != nil {
			return fail(err)
		}
		store, _ := got["store"].(map[string]any)
		var cards []string
		for _, x := range caches.ListOf(got["cards"]) {
			c, _ := x.(map[string]any)
			cards = append(cards, kit.Text(c["name"]))
		}
		if len(cards) == 0 {
			cards = []string{"no card"}
		}
		t.Printf("%s  %s  %s   the store: %s %s\n", on, kit.Text(got["platform"]), strings.Join(cards, ", "), kit.Text(store["seen"]), kit.Text(store["path"]))
		services, _ := got["services"].(map[string]any)
		for name, x := range services {
			e, _ := x.(map[string]any)
			key := kit.Text(e["key"])
			if key == "" {
				key = name
			}
			t.Printf("  %-24s %s  %s\n", key, kit.Text(e["state"]), kit.Text(e["why"]))
			for _, f := range kit.Strs(e["found"]) {
				t.Println("      · " + f)
			}
			for _, y := range caches.ListOf(e["lines"]) {
				line, _ := y.(map[string]any)
				note := ""
				if line["unknown_source"] == true {
					note = "  ( unknown source, named by the store )"
				}
				t.Println("      # " + kit.Text(line["comment"]) + note)
				t.Println("      " + kit.Text(line["command"]))
			}
		}
		return 0
	case "fetch":
		if len(words) != 1 {
			t.Sayln("errand serve fetch [--on M] URL#sha256=HASH | git:URL@COMMIT | --for NAME#V git+URL@COMMIT | --for NAME#V pip:PKG==V")
			return 2
		}
		req := map[string]any{"do": "fetch", "source": words[0], "source_ok": o.sources}
		for k, v := range map[string]string{"index": o.index, "into": o.into, "for": o.forKey} {
			if v != "" {
				req[k] = v
			}
		}
		got, err := o.asked(t, req)
		if err != nil {
			return fail(err)
		}
		where := kit.Text(got["path"])
		if got["path"] == nil {
			where = kit.Text(got["env"])
		}
		t.Printf("%s: %s\n", on, where)
		return 0
	case "probe":
		if len(o.command) == 0 {
			t.Sayln("errand serve probe [--on M] -- PROGRAM --version")
			return 2
		}
		got, err := o.asked(t, map[string]any{"do": "probe", "argv": o.command})
		if err != nil {
			return fail(err)
		}
		t.Printf("%s: %s   %s\n", o.command[0], kit.Text(got["version"]), kit.Text(got["output"]))
		return 0
	case "install":
		if len(words) != 1 || !strings.Contains(words[0], "#") {
			t.Sayln("errand serve install [--on M] [--gpus N] [--idle T] NAME#VERSION [-- WORDS]")
			return 2
		}
		req := map[string]any{"do": "install", "key": words[0], "yes": true}
		needs := map[string]any{}
		for k, v := range o.spec.Needs {
			needs[k] = v
		}
		if o.spec.Idle != 600 {
			needs["idle"] = o.spec.Idle
		}
		if len(needs) > 0 {
			req["needs"] = needs
		}
		if len(o.command) > 0 {
			req["words"] = o.command
		} else { // composed from the package there: shown, then asked
			composed, err := o.asked(t, map[string]any{"do": "compose", "key": words[0]})
			if err != nil {
				return fail(err)
			}
			t.Println(kit.Text(composed["text"]))
			if !o.yes {
				answer, ok := "", false
				if t.TtyIn && t.TtyOut {
					answer, ok = t.Ask("install it? [y/N] ")
				}
				if answer = strings.ToLower(answer); !ok || (answer != "y" && answer != "yes" && answer != "o" && answer != "oui") {
					t.Println("not installed")
					return 1
				}
			}
		}
		got, err := o.asked(t, req)
		if err != nil {
			return fail(err)
		}
		d, _ := got["declaration"].(map[string]any)
		note := ""
		if d["provider"] == nil {
			note = "   unverified: no package there provides this version"
		}
		t.Printf("%s: declared on %s in %s   %s%s\n", words[0], on, kit.Text(got["file"]), strings.Join(kit.Strs(d["command"]), " "), note)
		return 0
	}
	return 2
}

// versionsMain: `errand serve list --versions | pin | unpin | prune`
func versionsMain(t *reach.Term, o serveOptions) int {
	verb := "versions"
	if len(o.words) > 0 && o.words[0] != "list" {
		verb = o.words[0]
	}
	req := map[string]any{"do": verb}
	switch verb {
	case "pin", "unpin":
		if len(o.words) != 2 {
			t.Sayf("errand serve %s NAME#VERSION", verb)
			return 2
		}
		req["key"] = o.words[1]
	case "prune":
		req["dry_run"] = o.dryRun
		if o.unused != nil {
			req["unused"] = *o.unused
		}
	}
	got, err := o.asked(t, req)
	if err != nil {
		t.Sayln("errand serve "+verb+":", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return 1
	}
	switch verb {
	case "pin", "unpin":
		t.Printf("  %s  %sned\n", o.words[1], verb)
	case "prune":
		word := "removed"
		if o.dryRun {
			word = "would go"
		}
		n := 0
		for _, k := range caches.ListOf(got["versions"]) {
			t.Printf("  %s: %s\n", kit.Text(k), word)
			n++
		}
		for _, x := range caches.ListOf(got["code"]) {
			c, _ := x.(map[string]any)
			t.Printf("  code %s: %s\n", kit.Text(c["path"]), word)
			n++
		}
		if n == 0 {
			t.Println("  nothing to remove")
		}
	default:
		now := kit.Seconds(time.Now())
		versions, code := caches.ListOf(got["versions"]), caches.ListOf(got["code"])
		if len(versions)+len(code) == 0 {
			t.Println("  no version of a service here")
		}
		for _, x := range versions {
			v, _ := x.(map[string]any)
			var said []string
			add := func(s string) {
				if s != "" {
					said = append(said, s)
				}
			}
			add(kit.Text(v["state"]))
			if n := kit.Num(v["size"], 0); n > 0 {
				add(caches.Human(n))
			}
			if last := kit.Num(v["last"], 0); last > 0 {
				add("used " + reach.Span(now-last) + " ago")
			} else {
				add("never used")
			}
			if n := len(caches.ListOf(v["stores"])); n > 0 {
				add(fmt.Sprintf("asked by %d store%s", n, reach.Plural(n)))
			} else {
				add("no store asks for it")
			}
			if v["pinned"] == true {
				add("pinned")
			}
			if v["unused"] == true {
				add("goes at the next purge")
			}
			t.Printf("  %-28s %s\n", kit.Text(v["key"]), strings.Join(said, " · "))
		}
		for _, x := range code {
			c, _ := x.(map[string]any)
			t.Printf("  code %s@%.7s  %s\n", filepath.Base(kit.Text(c["git"])), kit.Text(c["rev"]), caches.Human(kit.Num(c["size"], 0)))
		}
	}
	return 0
}

// keepForward: a port the system keeps, and the forward started at its first connection
func keepForward(t *reach.Term, o serveOptions, h *reach.HostSpec, name string, port int) int {
	label := "dev.errand.forward." + runinfo.NameOf(name)
	if o.off {
		if units.UnkeepPort(label) {
			t.Println("  " + name + ": the port given back")
		} else {
			t.Println("  " + name + ": was not kept")
		}
		return 0
	}
	if port == 0 {
		t.Sayln("errand serve forward --keep: the port to keep, --port N ( the one your clients know )")
		return 2
	}
	argv := []string{reach.TypedPath(os.Getenv), "kept-forward", name, h.Host}
	if err := units.KeepPort(label, append(argv, h.Options...), port); err != nil {
		t.Sayln("errand serve forward --keep:", err)
		return 1
	}
	t.Printf("http://127.0.0.1:%d   -> %s on %s, kept by the system: errand starts at the first connection\n", port, name, h.Host)
	return 0
}

// KeptForwardMain: what the system starts for a port it keeps
func KeptForwardMain(args []string) int {
	if len(args) < 2 {
		return 2
	}
	lis, err := units.InheritedListener()
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand serve forward: no socket from the system")
		return 2
	}
	h := &reach.HostSpec{Host: args[1], Options: args[2:]}
	t := &reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}
	conns := forwardTo(lis, func() (*reach.Link, error) { return reach.LinkTo(t, h) }, args[0], "", func(s string) { fmt.Fprintln(os.Stderr, s) })
	for last := time.Now(); time.Since(last) < 10*time.Minute; time.Sleep(time.Second) {
		if conns() > 0 {
			last = time.Now()
		}
	}
	return 0
}
