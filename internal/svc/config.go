// What a person says of this machine, for the services of pages ( ADR 0028 §3, 0040 ): `~/.config/errand/config.json`
//
//	{ "sources": [ "https://…" ], "dev_sources": { "github.com/sdot-team/sdot": { "path": "~/Projects/sdot-ffi", "env": "prj-jax" } } }

package svc

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

type Config struct {
	Sources []string             `json:"sources"` // where code may be taken from without a second word; nil: the usual ones
	Dev     map[string]DevSource `json:"dev_sources"`
}

// DevSource is a checkout that stands for a repository on this machine.
type DevSource struct {
	Path string            `json:"path"`
	Env  string            `json:"env,omitempty"`
	Envs map[string]string `json:"envs,omitempty"` // for its copies, by machine
}

func ConfigFile() string { return filepath.Join(configDir(nil), "config.json") }

func ReadConfig() Config {
	var c Config
	_ = reach.DecodeInto(caches.ReadObject(ConfigFile()), &c)
	return c
}

var configuring sync.Mutex

// ChangeConfig: one part of it, changed; the rest stays as it is, known to this errand or not
func ChangeConfig(change func(*Config)) error {
	configuring.Lock()
	defer configuring.Unlock()
	all := caches.ReadObject(ConfigFile())
	if all == nil {
		all = map[string]any{}
	}
	c := ReadConfig()
	if c.Dev == nil {
		c.Dev = map[string]DevSource{}
	}
	change(&c)
	all["dev_sources"] = c.Dev
	if c.Sources != nil {
		all["sources"] = c.Sources
	}
	if err := os.MkdirAll(filepath.Dir(ConfigFile()), 0o755); err != nil {
		return err
	}
	return caches.WriteObject(ConfigFile(), all, true)
}

// NotRead: the file of declarations of before, if it is there
func NotRead() string {
	if f := filepath.Join(configDir(nil), "services.py"); kit.Exists(f) {
		return f + " is not read any more: `errand serve [options] -- COMMAND` says all of a server ( ADR 0040 ), `errand serve dev` and `source` what was said there of checkouts and sources"
	}
	return ""
}
