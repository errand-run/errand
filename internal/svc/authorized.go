// What may be asked for here by those who may not say a command ( ADR 0040 ): a page, an ssh key meant for one server.
// `~/.config/errand/authorized-services.json`, of its user alone:
//
//	{ "3f2a91c0": { "label": "ollama", "definition": { "command": [ … ], … }, "to": [ "key:boucles" ], "since": "…", "used": "…" },
//	  "63ecc287": { "label": "blender#5b1e…", "definition": { … }, "to": [ "pages" ], "page": "blender#5b1e…", "provider": "…" } }
//
// The key is the hash of the definition, read on this machine when it was authorized: what a restricted asker gives. A page
// gives the portable `NAME#VERSION` of its store instead ( `page` ), which leads here to a definition with this machine's
// paths. What a user asks for by their own link does not come here.

package svc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/system"
)

// Authorization is one definition, and to whom it is given.
type Authorization struct {
	Hash       string         `json:"-"`
	Label      string         `json:"label"`
	Definition map[string]any `json:"definition"`          // as `errand serve --spec -` reads it
	To         []string       `json:"to"`                  // "pages", "key:NAME"
	Page       string         `json:"page,omitempty"`      // for pages: NAME~HASH, the description's ( ADR 0041 )
	Described  map[string]any `json:"described,omitempty"` // ... that description, as the page sent it
	Dev        string         `json:"dev,omitempty"`       // ... in development: the checkout it runs from
	Pinned     bool           `json:"pinned,omitempty"`    // kept out of `errand serve prune`
	Since      string         `json:"since"`
	Used       string         `json:"used,omitempty"`
}

const stamp = "2006-01-02 15:04:05"

var toWhom = regexp.MustCompile(`^(pages|key:[A-Za-z0-9_.@-]+)$`)

// CheckTo: "pages", or "key:NAME"
func CheckTo(to string) error {
	if !toWhom.MatchString(to) {
		return fmt.Errorf("--to pages, or --to key:NAME ( got %q )", to)
	}
	return nil
}

// configDir: errand's, of this user and instance ( `$XDG_CONFIG_HOME/errand/<instance>` )
func configDir(environ []string) string {
	if len(environ) == 0 {
		environ = os.Environ()
	}
	return system.ConfigDir(environ, kit.EnvOf)
}

func AuthorizedFile() string { return filepath.Join(configDir(nil), "authorized-services.json") }

// Authorized: what is authorized here, by hash; what cannot be read of it is said
func Authorized() (map[string]*Authorization, error) {
	out := map[string]*Authorization{}
	data, err := os.ReadFile(AuthorizedFile())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	if err != nil {
		return map[string]*Authorization{}, fmt.Errorf("%s: %v", AuthorizedFile(), err)
	}
	for h, a := range out {
		if a == nil {
			delete(out, h)
			continue
		}
		a.Hash = h
	}
	return out, nil
}

var authorizing sync.Mutex

// changeAuthorized: changed and written again, for its user alone
func changeAuthorized(change func(map[string]*Authorization) error) error {
	authorizing.Lock()
	defer authorizing.Unlock()
	all, err := Authorized()
	if err != nil {
		return err
	}
	if err := change(all); err != nil {
		return err
	}
	f := AuthorizedFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(all, "", " ")
	if err != nil {
		return err
	}
	part := filepath.Join(filepath.Dir(f), fmt.Sprintf(".%s.%d.part", filepath.Base(f), os.Getpid()))
	if err := os.WriteFile(part, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(part, f)
}

// Definition: a server's definition as data -- the request that asks for it
func Definition(s ServiceSpec) map[string]any {
	out := map[string]any{"command": s.Command}
	if len(s.Env) > 0 {
		out["vars"] = s.Env
	}
	if s.Cwd != "" {
		out["cwd"] = s.Cwd
	}
	for _, k := range []string{"gpus", "gpu_ram", "ram", "cpus"} {
		if v := s.Needs[k]; v != nil {
			out[k] = v
		}
	}
	if s.Exclusive {
		out["exclusive"] = true
	}
	if len(s.Ports) > 1 || len(s.Ports) == 1 && (s.Ports[0].Name != "port" || s.Ports[0].Announced > 0 || s.Ports[0].Server > 0) {
		ports := []any{}
		for _, p := range s.Ports {
			var side any
			if p.Announced > 0 || p.Server > 0 {
				side = fmt.Sprintf("%s:%s", portSide(p.Announced), portSide(p.Server))
			}
			ports = append(ports, []any{p.Name, side})
		}
		out["ports"] = ports
	}
	if s.Others {
		out["others"] = true
	}
	if s.Listen != "" {
		out["listen"] = s.Listen
	}
	if s.Always {
		out["always"] = true
	}
	for k, v := range map[string]any{"busy": s.Busy, "ready": s.Ready} {
		if v != nil && v != "" {
			out[k] = v
		}
	}
	for k, v := range map[string]float64{"idle": s.Idle, "grace": s.Grace, "keep": s.Keep, "hold_end": s.HoldEnd} {
		if v > 0 {
			out[k] = v
		}
	}
	if s.Label != "" {
		out["label"] = s.Label
	}
	if s.Version != "" {
		out["version"] = s.Version
	}
	if len(s.VersionOf) > 0 {
		out["version_of"] = s.VersionOf
	}
	if len(s.On) > 0 {
		out["on"] = s.On
	}
	return out
}

func portSide(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

// Spec: the server it authorizes
func (a Authorization) Spec() (ServiceSpec, error) {
	b, err := json.Marshal(a.Definition)
	if err != nil {
		return ServiceSpec{}, err
	}
	s, machine, err := ParseRequest(b)
	if err != nil {
		return s, fmt.Errorf("authorization %s: %v", a.Hash, err)
	}
	if machine != "" {
		return s, fmt.Errorf("authorization %s: it names a machine ( %s ): what is authorized runs where it is written", a.Hash, machine)
	}
	s.Label, s.Name = a.Label, a.Label
	s.Source = "authorized"
	s.Complete()
	return s, nil
}

// Command: what it runs, as it reads
func (a Authorization) Command() []string { return kit.Strs(a.Definition["command"]) }

// Authorize: a server given to `to` -> its hash. A page's: `how.Page` is what the page asks for, and leads to this
// definition alone from now on.
func Authorize(s ServiceSpec, to string, how Authorization) (string, error) {
	if err := CheckTo(to); err != nil {
		return "", err
	}
	if (to == "pages") != (how.Page != "") {
		return "", errors.New("a page describes what it asks for, and only a page does: --to pages --spec -, the description on stdin")
	}
	s.Complete()
	if err := s.Check(); err != nil {
		return "", err
	}
	machine, _, err := s.Where()
	if err != nil {
		return "", err
	}
	if machine != nil {
		return "", fmt.Errorf("it runs on %s: it is authorized there ( errand serve authorize --on %s … )", machine.String("host"), machine.String("host"))
	}
	hash, err := s.Hash()
	if err != nil {
		return "", err
	}
	label := s.Label
	if label == "" {
		label = s.Name
	}
	return hash, changeAuthorized(func(all map[string]*Authorization) error {
		if how.Page != "" { // what the page asked for led elsewhere: no more
			for h, a := range all {
				if a.Page == how.Page && h != hash {
					a.To = slices.DeleteFunc(a.To, func(w string) bool { return w == "pages" })
					if len(a.To) == 0 {
						delete(all, h)
					}
				}
			}
		}
		a := all[hash]
		if a == nil {
			a = &Authorization{Since: time.Now().Format(stamp), To: []string{}}
			all[hash] = a
		}
		a.Label, a.Definition = label, Definition(s)
		if !slices.Contains(a.To, to) {
			a.To = append(a.To, to)
		}
		if how.Page != "" {
			a.Page, a.Described, a.Dev = how.Page, how.Described, how.Dev
		}
		return nil
	})
}

// Withdraw: an authorization taken back -- from one, or from all when `to` is "" -> whether there was one
func Withdraw(hash, to string) (bool, error) {
	found := false
	return found, changeAuthorized(func(all map[string]*Authorization) error {
		a := all[hash]
		if a == nil {
			return fmt.Errorf("no authorization %s on %s", hash, queue.Host())
		}
		n := len(a.To)
		if a.To = slices.DeleteFunc(a.To, func(w string) bool { return to == "" || w == to }); len(a.To) == n {
			return fmt.Errorf("authorization %s is not given to %s ( %s )", hash, to, strings.Join(a.To, ", "))
		}
		found = true
		if len(a.To) == 0 {
			delete(all, hash)
		}
		return nil
	})
}

// AuthorizedTo: the server of a hash, when it is given to `to` ( "": to anyone ) -- and its authorization
func AuthorizedTo(hash, to string) (ServiceSpec, *Authorization, error) {
	all, err := Authorized()
	if err != nil {
		return ServiceSpec{}, nil, err
	}
	a := all[hash]
	switch {
	case a == nil:
		return ServiceSpec{}, nil, fmt.Errorf("no server %s is authorized on %s", hash, queue.Host())
	case to != "" && !slices.Contains(a.To, to):
		return ServiceSpec{}, nil, fmt.Errorf("server %s is not authorized to %s on %s", hash, to, queue.Host())
	}
	s, err := a.Spec()
	return s, a, err
}

// PageAuthorized: what pages may ask for here, by `NAME~HASH`
func PageAuthorized() map[string]*Authorization {
	all, _ := Authorized()
	out := map[string]*Authorization{}
	for _, a := range all {
		if a.Page != "" && slices.Contains(a.To, "pages") {
			out[a.Page] = a
		}
	}
	return out
}

// PageServices: the servers pages may ask for here, by `NAME~HASH`, and what cannot be read
func PageServices() (map[string]ServiceSpec, []string) {
	out, said := map[string]ServiceSpec{}, []string{}
	if _, err := Authorized(); err != nil {
		said = append(said, err.Error())
	}
	for k, a := range PageAuthorized() {
		s, err := a.Spec()
		if err == nil {
			err = s.Check()
		}
		if err != nil {
			said = append(said, fmt.Sprintf("%s: %v", k, err))
			continue
		}
		out[k] = s
	}
	sort.Strings(said)
	return out, said
}

// UsedNow: an authorization was used -- written at most every ten minutes
func UsedNow(hash string) {
	all, _ := Authorized()
	if a := all[hash]; a == nil || a.Used != "" && time.Since(parseStamp(a.Used)) < 10*time.Minute {
		return
	}
	_ = changeAuthorized(func(all map[string]*Authorization) error {
		if a := all[hash]; a != nil {
			a.Used = time.Now().Format(stamp)
		}
		return nil
	})
}

// PinPage: a page's version kept out of the purge, or not any more
func PinPage(key string, on bool) error {
	return changeAuthorized(func(all map[string]*Authorization) error {
		known := []string{}
		for _, a := range all {
			if a.Page == key {
				a.Pinned = on
				return nil
			}
			if a.Page != "" {
				known = append(known, a.Page)
			}
		}
		sort.Strings(known)
		return errors.New(key + " is not authorized here ( for pages: " + strings.Join(known, ", ") + " )")
	})
}

// dropPage: what a page asked for, authorized no more
func dropPage(key string) error {
	return changeAuthorized(func(all map[string]*Authorization) error {
		for h, a := range all {
			if a.Page == key {
				delete(all, h)
			}
		}
		return nil
	})
}

func parseStamp(s string) time.Time {
	t, _ := time.ParseInLocation(stamp, s, time.Local)
	return t
}
