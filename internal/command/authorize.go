// `errand serve authorize | authorized | withdraw`: what may be asked for on a machine by those who may not say a command, a
// page or an ssh key ( ADR 0040 ).

package command

import (
	"errors"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/system"
)

const authorizeUsage = `errand serve authorize --to key:NAME [--on M] [options] -- COMMAND
errand serve authorize --to pages [--on M] [--path TOOL=PATH] [--source URL] [--yes] --spec -    ( a page's description on stdin )`

// serveAuthorize: a server given to a key or to pages, on the machine it runs on
func serveAuthorize(t *reach.Term, o serveOptions, words []string) int {
	fail := func(err error) int {
		t.Sayln("errand serve authorize:", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return 1
	}
	switch {
	case len(words) > 0 || len(o.command) == 0 && o.request == "":
		t.Sayln(authorizeUsage)
		return 2
	case o.to == "":
		t.Sayln("errand serve authorize: to whom? --to key:NAME, or --to pages")
		return 2
	case o.to == "pages" && (o.request == "" || len(o.command) > 0):
		t.Sayln("errand serve authorize: a page describes its service: --to pages --spec -, the description on stdin ( ADR 0041 )")
		return 2
	}
	if err := svc.CheckTo(o.to); err != nil {
		return fail(err)
	}
	on := o.host
	if on == "" {
		on = "here"
	}
	if o.to == "pages" {
		return authorizePage(t, o, on, fail)
	}
	o.words = nil // ( `authorize` names nothing )
	spec, err := askedInFull(t, o)
	if err == nil {
		err = spec.Check()
	}
	if err != nil {
		return fail(err)
	}
	_, there, err := spec.Where() // ( written on the machine it runs on, as it runs there )
	if err != nil {
		return fail(err)
	}
	got, err := o.asked(t, map[string]any{"do": "authorize", "spec": there, "to": o.to})
	if err != nil {
		return fail(err)
	}
	hash := kit.Text(got["hash"])
	t.Printf("%s  %s authorized to %s on %s   %s\n", hash, there.Name, o.to, on, svc.Typed(there.Command))
	if name, ok := strings.CutPrefix(o.to, "key:"); ok {
		t.Println("  in ~/.ssh/authorized_keys there, for the key " + name + ", one of:")
		stable := "~/.cache/errand/" + system.Instance() + "/bin/errand" // ( the instance's stable name there; its directory, where it is declared elsewhere )
		t.Println(`    restrict,command="` + stable + ` there --only ` + o.to + `" ssh-ed25519 …      ( an errand on the other side )`)
		t.Println(`    restrict,command="` + stable + ` serve connect ` + hash + `" ssh-ed25519 …      ( this server alone, by its standard input )`)
	}
	return 0
}

// authorizePage: a page's description, shown as the machine would run it, then authorized there once a person said yes
func authorizePage(t *reach.Term, o serveOptions, on string, fail func(error) int) int {
	var data []byte
	for more := true; more; {
		var b []byte
		select {
		case b, more = <-t.Stdin:
			data = append(data, b...)
		case <-t.Gone:
			return 1
		}
	}
	d, _, err := svc.ParsePageDesc(data)
	if err != nil {
		return fail(err)
	}
	paths := map[string][]string{}
	for _, p := range o.paths {
		tool, at, _ := strings.Cut(p, "=")
		paths[tool] = append(paths[tool], at)
	}
	got, err := o.asked(t, map[string]any{"do": "page_look", "described": d, "paths": paths})
	if err != nil {
		return fail(err)
	}
	var l svc.PageLook
	if err := reach.DecodeInto(got["look"], &l); err != nil {
		return fail(err)
	}
	t.Printf("%s on %s: %s\n", l.Key, on, l.State)
	for _, f := range l.Found {
		t.Println("  · " + f)
	}
	for _, n := range l.Missing {
		from := ""
		if n.Source != "" {
			from = "  from " + n.Source
			if !n.Trusted {
				from += "  ( an unknown source: --source " + n.Source + " to accept it )"
			}
		}
		t.Println("  + " + n.What + from)
	}
	for _, p := range l.Probes {
		t.Printf("  ? %s: errand serve probe --on %s -- %s\n", p.Tool, on, svc.Typed(p.Argv))
	}
	switch l.State {
	case "unavailable":
		return fail(errors.New(l.Why))
	case "authorized":
		return 0
	}
	t.Println("  runs: " + svc.Typed(l.Command))
	if !o.yes {
		answer, ok := "", false
		if t.TtyIn && t.TtyOut {
			answer, ok = t.Ask("fetch what is missing, and authorize it to pages? [y/N] ")
		}
		if answer = strings.ToLower(answer); !ok || (answer != "y" && answer != "yes" && answer != "o" && answer != "oui") {
			t.Println("not authorized ( --yes to say it beforehand )")
			return 1
		}
	}
	if got, err = o.asked(t, map[string]any{"do": "authorize", "described": d, "paths": paths, "source_ok": o.sources}); err != nil {
		return fail(err)
	}
	t.Printf("%s authorized to pages on %s\n", l.Key, on)
	return 0
}

// serveAuthorized: `errand serve authorized [--on M]`, `errand serve withdraw [--on M] HASH [--to WHO]`
func serveAuthorized(t *reach.Term, o serveOptions, verb string, words []string) int {
	fail := func(err error) int {
		t.Sayln("errand serve "+verb+":", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return 1
	}
	if verb == "withdraw" {
		if len(words) != 1 {
			t.Sayln("errand serve withdraw [--on M] HASH [--to pages|key:NAME]")
			return 2
		}
		if _, err := o.asked(t, map[string]any{"do": "withdraw", "hash": words[0], "to": o.to}); err != nil {
			return fail(err)
		}
		whom := "anyone"
		if o.to != "" {
			whom = o.to
		}
		t.Printf("%s: authorized to %s no more ( a connection already open is not cut )\n", words[0], whom)
		return 0
	}
	if len(words) > 0 {
		t.Sayln("errand serve authorized [--on M]")
		return 2
	}
	got, err := o.asked(t, map[string]any{"do": "authorized"})
	if err != nil {
		return fail(err)
	}
	all := map[string]svc.Authorization{}
	if err := reach.DecodeInto(got["authorized"], &all); err != nil {
		return fail(err)
	}
	hashes := make([]string, 0, len(all))
	for h := range all {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	if len(hashes) == 0 {
		t.Println("nothing is authorized ( " + kit.Text(got["file"]) + " )")
	}
	for _, h := range hashes {
		au := all[h]
		au.Hash = h
		used := "never used"
		if au.Used != "" {
			used = "used " + au.Used
		}
		t.Printf("%s  %-24s to %s   since %s, %s\n", h, au.Label, strings.Join(au.To, ", "), au.Since, used)
		if s, err := au.Spec(); err != nil {
			t.Println("      " + err.Error())
		} else {
			t.Println("      " + authorizedText(s))
		}
	}
	return 0
}

// authorizedText: a definition, as it reads -- the values of secret variables aside
func authorizedText(s svc.ServiceSpec) string {
	words := []string{}
	vars := s.ShownVars()
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := vars[k]
		if v != "…" {
			v = layer.Quote(v)
		}
		words = append(words, k+"="+v)
	}
	text := strings.TrimSpace(strings.Join(words, " ") + " " + svc.Typed(s.Command))
	if n := s.NeedsText(); n != "" {
		text += "   " + n
	}
	if s.Cwd != "" {
		text += "   in " + s.Cwd
	}
	return text
}

var errNoHash = errors.New("errand serve connect HASH [PORT]: the hash of an authorization ( errand serve authorized ), or of a lease")

func isHash(word string) bool {
	if len(word) != 8 {
		return false
	}
	for _, c := range word {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
