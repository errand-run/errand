package agent

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
)

// devEnsure: the declaration `NAME#dev` from the checkout, written again when its command or its files changed -- the
// running one is stopped then, and the next connection starts the new -> its key
func (a *Place) devEnsure(name string, want svc.AskedService) (string, error) {
	decl, err := svc.DeclaredServices(kit.LocalCache(), nil)
	if err != nil {
		return "", err
	}
	d := svc.DevFor(want, decl.Dev)
	if d == nil {
		return "", errors.New("no dev source here for " + name)
	}
	if d.Why != "" {
		return "", errors.New(d.Why)
	}
	p, err := d.Described(name, want)
	if err != nil {
		return "", err
	}
	words, err := d.Command(p, name)
	if err != nil {
		return "", err
	}
	sig, _ := p.Signature(name)
	key := name + "#dev"
	now := svc.InstalledHere(nil)[key]
	if reflect.DeepEqual(kit.Strs(now["command"]), words) && kit.Text(now["dev_sig"]) == sig {
		return key, nil
	}
	a.stopService(key)
	entry := p.Entry(name)
	needs := svc.DescribedNeeds(entry)
	for _, k := range []string{"gpus", "gpu_ram", "ram", "cpus"} {
		if needs[k] == nil && entry[k] != nil {
			needs[k] = entry[k]
		}
	}
	_, err = svc.DeclareHere(key, words, needs, "", map[string]any{"dev": d.Path, "dev_sig": sig})
	return key, err
}

// stopService: a service, whichever agent of this machine holds it
func (a *Place) stopService(name string) {
	s, other := a.holder(name)
	if other != "" {
		_, _ = reach.AskAgent(other, map[string]any{"op": "service_stop", "name": name})
	} else if s != nil {
		s.stop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration((s.spec.Grace+8)*float64(time.Second)))
		defer cancel()
		select {
		case <-s.done:
		case <-ctx.Done():
		}
	}
}
