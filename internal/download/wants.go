// The requests runs leave when they have no way to a URL ( download.go ), as a place's agent answers them ( ADR 0013,
// 0035 ). No link is kept for this: the agent looks, once a second, at the directories of the projects it has runs
// in -- a local listing.
//
// A request is answered by the first that has a way: this agent; the agent it depends on ( the machine's, for a
// container's ); whoever follows a run of that project from another machine, told by a note on the run's channel.
// With nobody that has a way, it stays where it is, and is tried again by this agent every minute.

package download

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

type WantsBook struct {
	Mu      sync.Mutex
	Tried   map[string]time.Time // when this agent last tried each
	Pending map[string]Want      // those nobody here has a way to: for whoever follows
}

// RelayWant: a request of a run followed from this machine, which the machine it runs on has no way to -- the file
// downloaded into the project here, and sent into the copy over there with the answer the run waits for
func RelayWant(t *reach.Term, l *reach.Link, note runinfo.RunNote, w Want) {
	where := note.There.Name
	t.Sayf("  %s: %s has no way to %s -- downloading it here", w.Name, where, w.URL)
	d := downloadsOf(note.Project)
	sha, keys := "", []string{}
	body := wantAnswer{}
	if found := haveDownload(d, w.URL, w.SHA256, w.Name); found != "" {
		sha = filesync.LastPart(filesync.Parent(slashed(found)))
	} else if unlock, err := lockDownload(d, w.URL); err != nil {
		body.Error = err.Error()
	} else {
		sha, err = FetchURL(w.URL, d, w.Name, w.SHA256, t.Environ, nil) // by the way out of whoever typed the command
		unlock()
		if err != nil {
			body.Error = "not from " + where + ", nor from the machine that follows the run ( " + err.Error() + " )"
		}
	}
	if sha != "" {
		rememberDownload(d, w.URL, sha, w.Name)
		keys, body.SHA256 = []string{sha + "/" + w.Name}, sha
	} else {
		t.Sayf("  %s: %s", w.Name, body.Error)
	}
	if err := filesync.SendFiles(l, d, w.Dir, keys, w.Answer, body); err != nil {
		t.Sayf("  %s: could not be sent to %s: %v", w.Name, where, err)
	}
}

func slashed(path string) string {
	out := []byte(path)
	for i, c := range out {
		if c == '\\' {
			out[i] = '/'
		}
	}
	return string(out)
}

func WantFrom(payload any) (Want, bool) {
	var w Want
	b, _ := json.Marshal(payload)
	return w, json.Unmarshal(b, &w) == nil && w.ID != ""
}
