package queue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A claim that grows ( ADR 0014 ). A run asks for more once it knows: `errand require`, from its own code. Its claim
// stays held by the process that took it; growing only rewrites what it holds, under the lock, once it fits. A
// growing claim is ahead of any new request. Two that grow at once can each hold what the other waits for: the
// younger is stopped ( ErrRerun ), and run again with what it requires up front.

func inMB(s Share, size float64) (float64, bool) {
	if s.Percent != 0 {
		return size * s.Percent / 100, size != 0
	}
	return s.MB, true
}

// merged: what a claim holds once it holds `more` too -- the larger of each. Cards held whole already hold any share
// of them. A share and a size are compared on a card of `size` MB; with no size to read the share with, what is held
// is kept: a claim never shrinks.
func merged(held, more Needs, size float64) Needs {
	out := Needs{CPUs: max(held.CPUs, more.CPUs), RAM: max(held.RAM, more.RAM), GPUs: max(held.GPUs, more.GPUs)}
	a, b := held.GPURam, more.GPURam
	if whole := held.GPUs > 0 && a.IsZero(); whole {
		return out
	}
	switch {
	case a.IsZero():
		out.GPURam = b
	case b.IsZero():
		out.GPURam = a
	case a.Percent != 0 && b.Percent != 0:
		out.GPURam = Share{Percent: max(a.Percent, b.Percent)}
	case a.Percent == 0 && b.Percent == 0:
		out.GPURam = Share{MB: max(a.MB, b.MB)}
	default:
		ma, oka := inMB(a, size)
		mb, okb := inMB(b, size)
		if out.GPURam = a; oka && okb && mb > ma {
			out.GPURam = b
		}
	}
	return out
}

// added: what a claim holds once `more` is added to it ( `require --extra` ) -- the sum of each. On a card, two
// shares add up, two sizes too, a share and a size on a card of `size` MB ( with none known, the cards are taken
// whole ). Cards held whole stay whole.
func added(held, more Needs, size float64) Needs {
	out := Needs{CPUs: held.CPUs + more.CPUs, RAM: held.RAM + more.RAM, GPUs: held.GPUs + more.GPUs}
	a, b := held.GPURam, more.GPURam
	if whole := held.GPUs > 0 && a.IsZero(); whole {
		return out
	}
	switch {
	case a.IsZero():
		out.GPURam = b
	case b.IsZero():
		out.GPURam = a
	case a.Percent != 0 && b.Percent != 0:
		out.GPURam = Share{Percent: min(a.Percent+b.Percent, 100)}
	default:
		ma, oka := inMB(a, size)
		mb, okb := inMB(b, size)
		if oka && okb {
			out.GPURam = Share{MB: ma + mb}
		}
	}
	return out
}

// referenceSize: the card a share and a size are compared on -- the smallest the claim holds, else the smallest of
// the machine, else 0 ( unknown )
func (q *Queue) referenceSize(c Claim) float64 {
	var sizes []float64
	for _, d := range c.Devices {
		sizes = append(sizes, q.Cap.cardSize(d))
	}
	if len(sizes) == 0 {
		sizes = q.Cap.CardRAM
	}
	best := 0.0
	for _, s := range sizes {
		if s > 0 && (best == 0 || s < best) {
			best = s
		}
	}
	return best
}

// Growth is what a run asks of its claim once it runs.
type Growth struct {
	Needs     Needs
	Exclusive bool
	Extra     bool // on top of what it was started with ( several add up ), not "at least that much in all"
	Say       func(string)
	Waiting   func(string)
}

// Grow: the claim at `path` made to hold what is asked too, waited for until it does. -> the cards it holds.
//
// What the code asked is kept in the claim -- Required, RequiredExtra, and the total it comes to, Wanted -- for the
// run's record, and for errand to remember what to claim up front next time.
func (q *Queue) Grow(ctx context.Context, path string, g Growth) ([]int, error) {
	asked := g.Needs
	if !(g.Extra && asked.GPUs == 0) { // ( `--extra --gpu-ram`: more memory on the cards held, not one more card )
		asked = asked.Normalized(1)
	}
	id := idOf(path)
	request := filepath.Join(q.sub("waiting"), id+".grow.json")
	waitedFor := ""
	defer func() {
		_ = os.Remove(request)
		if waitedFor != "" && g.Waiting != nil {
			g.Waiting("")
		}
	}()
	for {
		unlock, err := q.lock()
		if err != nil {
			return nil, err
		}
		row, ok := read(path)
		if !ok {
			unlock()
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), ErrGone)
		}
		size := q.referenceSize(row)
		base := row.Needs
		if row.Base != nil {
			base = *row.Base
		}
		var required, plus Needs
		if row.Required != nil {
			required = *row.Required
		}
		if row.RequiredExtra != nil {
			plus = *row.RequiredExtra
		}
		if g.Extra {
			plus = added(plus, asked, size)
		} else {
			required = merged(required, asked, size)
		}
		row.Base = &base
		if !required.IsZero() {
			row.Required = &required
		}
		if !plus.IsZero() {
			row.RequiredExtra = &plus
		}
		row.RequiredExcl = row.RequiredExcl || g.Exclusive
		// what the code comes to: what it started with and what was added, and at least what was asked
		wanted := merged(added(base, plus, size), required, size)
		row.Wanted = &wanted
		want := merged(row.Needs, wanted, size)
		excl := g.Exclusive || row.Exclusive
		if want == merged(row.Needs, Needs{}, size) && excl == row.Exclusive {
			err := writeJSON(path, row)
			unlock()
			return row.Devices, err
		}
		var others []Claim
		for _, o := range q.Held() {
			if o.Path != path {
				others = append(others, o)
			}
		}
		blocked, got := q.fits(want, excl, others, nil, row.Devices)
		if blocked == "" {
			if got == nil {
				got = []int{}
			}
			row.Needs, row.Exclusive, row.Devices, row.CardRAM = want, excl, got, q.shares(want, got, excl)
			err := writeJSON(path, row)
			unlock()
			return got, err
		}
		if q.youngerInACycle(row, id, others) {
			row.Rerun = true
			_ = writeJSON(path, row)
			unlock()
			return nil, fmt.Errorf("it waits for %s: %w", blocked, ErrRerun)
		}
		now := q.now()
		if os.Chtimes(request, now, now) != nil {
			w := Claim{Label: row.Label, Since: row.Since, Grows: true, Of: id, Exclusive: excl, Want: &want, Devices: []int{}}
			q.mine(&w)
			_ = writeJSON(request, w)
		}
		unlock()
		if blocked != waitedFor {
			if g.Say != nil {
				g.Say("  waiting for " + blocked)
			}
			if g.Waiting != nil {
				g.Waiting(fmt.Sprintf("room on %s: %s", q.Host, blocked))
			}
			waitedFor = blocked
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("stopped waiting for %s: %w", blocked, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// youngerInACycle: is this growing claim, waiting, among what holds back an older one that waits too? Then it is the
// one to stop ( and run again ): an older claim that grows is blocked only by younger ones that grow -- this one
// among them -- when it would fit without them, and does not with them. Two that wait for each other are the simplest
// such case; three or more growing to the whole machine ( benchmarks side by side ) are another, which no pair shows.
func (q *Queue) youngerInACycle(mine Claim, id string, others []Claim) bool {
	growing := map[string]Claim{}
	for _, w := range q.Waiting() {
		if w.Grows {
			growing[w.Of] = w
		}
	}
	everyone := append(append([]Claim{}, others...), mine)
	for name, req := range growing {
		if name == id || req.Want == nil {
			continue
		}
		var theirs *Claim
		for i := range others {
			if idOf(others[i].Path) == name {
				theirs = &others[i]
			}
		}
		if theirs == nil || theirs.Since > mine.Since {
			continue // gone, or younger than this one: not for this one to make way
		}
		var rest, all []Claim
		for _, o := range everyone {
			if o.Path == theirs.Path {
				continue
			}
			all = append(all, o)
			if _, grows := growing[idOf(o.Path)]; !(grows && o.Since > theirs.Since) {
				rest = append(rest, o)
			}
		}
		without, _ := q.fits(*req.Want, req.Exclusive, rest, nil, nil)
		with, _ := q.fits(*req.Want, req.Exclusive, all, nil, nil)
		if without == "" && with != "" {
			return true
		}
	}
	return false
}
