// `errand serve authorize | authorized | withdraw`: what may be asked for on a machine by those who may not say a command, a
// page or an ssh key ( ADR 0040 ).

package command

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
)

const authorizeUsage = `errand serve authorize --to key:NAME [--on M] [options] -- COMMAND
errand serve authorize [--to pages] [--on M] [options] NAME#VERSION [-- WORDS]`

// serveAuthorize: a server given to a key or to pages, on the machine it runs on
func serveAuthorize(t *reach.Term, o serveOptions, words []string) int {
	fail := func(err error) int {
		t.Sayln("errand serve authorize:", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return 1
	}
	if o.to == "" && len(words) == 1 {
		o.to = "pages"
	}
	switch {
	case len(words) > 1 || len(words) == 0 && len(o.command) == 0 && o.request == "":
		t.Sayln(authorizeUsage)
		return 2
	case o.to == "":
		t.Sayln("errand serve authorize: to whom? --to key:NAME, or --to pages NAME#VERSION")
		return 2
	case len(words) == 1 && o.to != "pages":
		t.Sayln("errand serve authorize: a page asks for NAME#VERSION, and only a page does: --to pages")
		return 2
	}
	if err := svc.CheckTo(o.to); err != nil {
		return fail(err)
	}
	on := o.host
	if on == "" {
		on = "here"
	}
	if len(words) == 1 { // a page's service
		return authorizePage(t, o, words[0], on, fail)
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
		t.Println(`    restrict,command="~/.cache/errand/bin/errand there --only ` + o.to + `" ssh-ed25519 …      ( an errand on the other side )`)
		t.Println(`    restrict,command="~/.cache/errand/bin/errand serve connect ` + hash + `" ssh-ed25519 …      ( this server alone, by its standard input )`)
	}
	return 0
}

func authorizePage(t *reach.Term, o serveOptions, key, on string, fail func(error) int) int {
	if !strings.Contains(key, "#") {
		t.Sayln(authorizeUsage)
		return 2
	}
	req := map[string]any{"do": "authorize", "page": key}
	needs := map[string]any{}
	for k, v := range o.spec.Needs {
		needs[k] = v
	}
	if o.spec.Idle != 600 {
		needs["idle"] = o.spec.Idle
	}
	if o.spec.Grace != 10 {
		needs["grace"] = o.spec.Grace
	}
	if o.spec.Keep > 0 {
		needs["keep"] = o.spec.Keep
	}
	if len(o.spec.Env) > 0 {
		needs["vars"] = o.spec.Env
	}
	if o.spec.Cwd != "" {
		needs["cwd"] = o.spec.Cwd
	}
	if o.spec.Busy != nil {
		needs["busy"] = o.spec.Busy
	}
	if o.spec.Exclusive {
		needs["exclusive"] = true
	}
	if len(needs) > 0 {
		req["needs"] = needs
	}
	if len(o.command) > 0 {
		req["words"] = o.command
	} else { // composed from the package there: shown, then asked
		composed, err := o.asked(t, map[string]any{"do": "compose", "key": key})
		if err != nil {
			return fail(err)
		}
		t.Println(kit.Text(composed["text"]))
		if !o.yes {
			answer, ok := "", false
			if t.TtyIn && t.TtyOut {
				answer, ok = t.Ask("authorize it to pages? [y/N] ")
			}
			if answer = strings.ToLower(answer); !ok || (answer != "y" && answer != "yes" && answer != "o" && answer != "oui") {
				t.Println("not authorized")
				return 1
			}
		}
	}
	got, err := o.asked(t, req)
	if err != nil {
		return fail(err)
	}
	var au svc.Authorization
	_ = reach.DecodeInto(got["authorization"], &au)
	note := ""
	if au.Unverified {
		note = "   unverified: no package there provides this version"
	}
	t.Printf("%s  %s authorized to pages on %s   %s%s\n", kit.Text(got["hash"]), key, on, svc.Typed(au.Command()), note)
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
	var refused []svc.Refusal
	_ = reach.DecodeInto(got["refused"], &refused)
	if len(refused) > 0 {
		t.Println("refused lately:")
	}
	for _, r := range refused {
		at := time.Unix(int64(r.At), 0).Format("2006-01-02 15:04")
		times := ""
		if r.Times > 1 {
			times = fmt.Sprintf(" ( %d times )", r.Times)
		}
		t.Printf("  %s  %s asked for %s%s: %s\n", at, r.Who, r.What, times, kit.FirstLine(r.Why))
		if len(r.Command) > 0 {
			t.Println("      would run: " + svc.Typed(r.Command))
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
