package agent

import (
	"errors"
	"reflect"

	"github.com/errand-run/errand/internal/svc"
)

// devEnsure: `NAME#dev` authorized to pages, from the checkout ( the person named it in dev_sources ); authorized again when its
// command or its files changed -- the running one is stopped then, and the next connection starts the new -> its key
func (a *Place) devEnsure(name string, want svc.AskedService) (string, error) {
	d := svc.DevFor(want, svc.ReadConfig().Dev)
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
	if now := svc.PageAuthorized()[key]; now != nil && reflect.DeepEqual(now.Command(), words) && now.DevSig == sig {
		return key, nil
	}
	a.stopServices(key, "", "", false)
	entry := p.Entry(name)
	needs := svc.DescribedNeeds(entry)
	for _, k := range []string{"gpus", "gpu_ram", "ram", "cpus"} {
		if needs[k] == nil && entry[k] != nil {
			needs[k] = entry[k]
		}
	}
	s, err := svc.PageSpec(key, words, needs)
	if err != nil {
		return key, err
	}
	_, err = svc.Authorize(s, "pages", svc.Authorization{Page: key, Dev: d.Path, DevSig: sig})
	return key, err
}
