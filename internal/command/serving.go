package command

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
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

// pageServicesMain: `errand serve fetch | probe`
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

// serveConfig: what a person says of this machine for the services of pages -- a checkout that stands for a repository, a source
// code may be taken from
func serveConfig(t *reach.Term, o serveOptions, verb string, words []string) int {
	usage := "errand serve source URL [--off]"
	if verb == "dev" {
		usage = "errand serve dev REPOSITORY PATH [--env ENV], errand serve dev REPOSITORY --off"
	}
	if len(words) == 0 || (verb == "source" && len(words) > 1) || (verb == "dev" && !o.off && len(words) != 2) {
		t.Sayln(usage)
		return 2
	}
	err := svc.ChangeConfig(func(c *svc.Config) {
		switch {
		case verb == "dev" && o.off:
			delete(c.Dev, words[0])
		case verb == "dev":
			was := c.Dev[words[0]]
			was.Path, was.Env = words[1], o.env
			c.Dev[words[0]] = was
		default:
			if c.Sources == nil {
				c.Sources = svc.UsualSources()
			}
			c.Sources = slices.DeleteFunc(c.Sources, func(s string) bool { return s == words[0] })
			if !o.off {
				c.Sources = append(c.Sources, words[0])
			}
		}
	})
	if err != nil {
		t.Sayln("errand serve:", err)
		return 1
	}
	t.Println("  " + svc.ConfigFile())
	return 0
}
