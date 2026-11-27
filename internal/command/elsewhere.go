// A run on another machine ( the guide's "Ssh: running elsewhere" ): what `errand run` does when an environment's
// first layer is `Ssh`.
//
// The project is pushed to its copy over there; that machine's agent is told what the run is, and starts it as one
// of its jobs -- the rest of the environment's layers, its place in that machine's queue, its record; this command
// follows the job through the link; and what the run wrote is brought back as its last step. A run launched with `--batch`, or whose connection broke, stays over there until something collects it:
// `errand attach`, `errand status`.

package command

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

const downloadsZone = "errand-cache" // `errand.download`'s cache zone, every project's ( ADR 0013 )

// projectSelection: what a push sends -- the project, minus what is no part of a copy, minus its zones: the
// project's own, what is built over there, and errand's outputs, each side's own
func projectSelection(doc *project.Document) (*filesync.Selection, error) {
	zones := []filesync.Zone{{Pattern: "errand-outputs", Kind: "errand"}, {Pattern: ".errand", Kind: "errand"}, {Pattern: "/" + downloadsZone, Kind: "cache"}}
	if rel, err := filepath.Rel(doc.Root, doc.OutputsDir()); err == nil && !strings.HasPrefix(rel, "..") && rel != "errand-outputs" {
		zones = append(zones, filesync.Zone{Pattern: "/" + filepath.ToSlash(rel), Kind: "errand"})
	}
	all := doc.AllZones()
	names := make([]string, 0, len(all))
	for p := range all {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		zones = append(zones, filesync.Zone{Pattern: p, Kind: all[p]})
	}
	// what the layers of ALL the project's environments build into it, by its exact path: environments share a copy
	kept := map[string]bool{}
	for _, env := range doc.Envs {
		if len(env.Stack) > 0 && env.Stack[0]["windows"] != nil { // what a layer brings back is written over there
			for _, p := range env.Stack[0].Strings("fetch") {
				zones = append(zones, filesync.Zone{Pattern: "/" + strings.Trim(filepath.ToSlash(p), "/"), Kind: "back"})
			}
		}
		for _, p := range layer.Kept(env.Stack) {
			kept[p] = true
		}
	}
	paths := make([]string, 0, len(kept))
	for p := range kept {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		zones = append(zones, filesync.Zone{Pattern: "/" + p, Kind: "build"})
	}
	for _, p := range runinfo.BuiltThere {
		zones = append(zones, filesync.Zone{Pattern: p, Kind: "build"})
	}
	return filesync.NewSelection(append(append([]string{}, runinfo.NotCopied...), doc.Settings.Exclude...), false, zones)
}

// zonesOf: the project's zones of a kind that name one path -- those a command brings back. One named anywhere ( no
// leading `/` ) is not looked for.
func zonesOf(doc *project.Document, kind string) []string {
	var out []string
	for p, k := range doc.AllZones() {
		if k == kind && strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "*?[") {
			out = append(out, strings.Trim(p, "/"))
		}
	}
	sort.Strings(out)
	return out
}

// pushTo: the machine an environment's first layer reaches, reached, and the project pushed to its copy there
// -> the link, how the machine is reached, the copy's root there
func pushTo(t *reach.Term, doc *project.Document, envName string, stack []project.Layer, loud, verbose, force bool) (*reach.Link, reach.HostSpec, string, error) {
	ssh, root := stack[0], doc.Root
	h := reach.HostOf(ssh)
	if h.Host == "" {
		return nil, h, "", fmt.Errorf("the environment %s: its Ssh layer names no host", envName)
	}
	for _, l := range stack[1:] {
		if l.Type() == "Ssh" {
			return nil, h, "", fmt.Errorf("the environment %s: Ssh must be the first layer, and the only one", envName)
		}
	}
	remoteRoot := ssh.String("root")
	if remoteRoot == "" {
		remoteRoot = root // the same path as here
	}
	sel, err := projectSelection(doc)
	if err != nil {
		return nil, h, "", err
	}
	l, err := reach.LinkTo(t, &h)
	if err != nil {
		return nil, h, "", err
	}
	if _, err := l.Ask(map[string]any{"op": "prepare", "root": remoteRoot, "outputs": ssh.String("errand_outputs_directory")}); err != nil {
		if ssh.String("root") == "" { // the same path as here, which that machine may not have
			err = fmt.Errorf("%w\n  the project goes to the same path as here unless its Ssh layer says where: Ssh( host = %q, root = \"…\" ), from the home folder there", err, h.Host)
		}
		return nil, h, "", err
	}
	if got, err := l.Ask(map[string]any{"op": "hello"}); err == nil {
		reach.UsedMachine(h, kit.Text(got["host"]), kit.Text(got["machine"]))
	}
	since := time.Now()
	done := t.Working("work", "↑ push → "+h.Host) // under way: on the place line's right, and gone when it is over
	sent, err := filesync.Push(l, root, remoteRoot, sel, filepath.Join(doc.OutputsDir(), "state", "sync", h.Key()+".json"), h.Host, force, t.Say)
	done()
	if err == nil {
		t.PlaceNumbers = fmt.Sprintf("↑ %d file%s · %s · %s", len(sent.Sent), reach.Plural(len(sent.Sent)), runinfo.Sized(sent.Bytes), runinfo.Took(time.Since(since).Seconds()))
	}
	if err != nil {
		return nil, h, "", fmt.Errorf("the project could not be pushed to %s:%s: %w", h.Host, remoteRoot, err)
	}
	for i, name := range sent.Deleted { // what errand had written there and the project has no more: nothing to look at
		if !verbose {
			break
		}
		if i == 20 {
			t.Say(fmt.Sprintf("    ...and %d more", len(sent.Deleted)-20))
			break
		}
		t.Say("    removed there, as here: " + name)
	}
	if loud && (len(sent.Sent) > 0 || verbose) && !t.PlaceOpen { // ( on a place line, its numbers are said there )
		t.Say(runinfo.Counted("  ↑ push → "+h.Host+":"+remoteRoot, sent, since))
	}
	for _, other := range ssh.Strings("also") { // `../loom` lands at `<remote root>/../loom`: the same layout as here
		plain, _ := filesync.NewSelection(runinfo.NotCopied, false, []filesync.Zone{{Pattern: "errand-outputs", Kind: "errand"}})
		to := path.Join(remoteRoot, filepath.ToSlash(other))
		done := t.Working("work", "↑ push "+other+" → "+h.Host)
		_, err := filesync.Push(l, filepath.Join(root, other), to, plain, "", h.Host, force, t.Say)
		done()
		if err != nil {
			return nil, h, "", fmt.Errorf("%s could not be pushed to %s:%s: %w", other, h.Host, to, err)
		}
	}
	return l, h, remoteRoot, nil
}

// runThere: the run, on the machine its environment's first layer reaches
// placed is where the runs of an environment go, once reached: the way to its machine's agent, and the project there.
type placed struct {
	l    *reach.Link
	h    *reach.HostSpec // nil: this machine
	root string
}

// reach: the other machine of a stack, with the project's copy there as it is here -> it, or how the command ends
func (r aRun) reach(stack []project.Layer, tags map[string]any) (*placed, int) {
	t, o, doc := r.t, r.o, r.doc
	ssh := stack[0]
	loud := !o.quiet
	// where it goes, drawn before it is there, with what is done to get there on its right ( ADR 0016 )
	if drawn := loud && viewOf(o).draws(t); drawn {
		where := ssh.String("host")
		if d := layer.Describe(stack[1:]); d != "" && d != "here" {
			where += " → " + d
		}
		t.PlaceOpen = true
		t.Event(map[string]any{"place": where})
	}
	l, h, remoteRoot, err := pushTo(t, doc, r.envName, stack, loud, o.verbose, o.force)
	if t.PlaceOpen {
		t.Event(map[string]any{"placed": t.PlaceNumbers})
		t.PlaceOpen, t.PlaceNumbers = false, ""
	}
	if err != nil {
		t.Sayln("errand:", err)
		return nil, 1
	}
	if !o.noSetup { // an image that is built in another environment is made there, and brought here, first
		err := carryImages(t, doc, &h, l, map[string]any{"root": remoteRoot, "tags": tags}, stack[1:], false, o)
		if errors.Is(err, reach.ErrStopped) {
			return nil, 130
		}
		if err != nil {
			t.Sayln("errand:", err)
			return nil, 1
		}
	}
	return &placed{l: l, h: &h, root: remoteRoot}, 0
}

func (r aRun) runThere(spec runinfo.LaunchSpec) int {
	t, o, doc, root := r.t, r.o, r.doc, r.doc.Root
	ssh := spec.Stack[0]
	stop := func(err error) int {
		t.Sayln("errand:", err)
		return 1
	}
	if ssh["windows"] != nil && reach.HostOf(ssh).VM != nil && o.batch {
		return stop(errors.New("--batch: a machine errand boots goes when nobody uses it; a run cannot be left in it"))
	}
	// what the words designate is said from the project's root: the file is not at the same place over there
	first := r.words[0]
	if !filepath.IsAbs(first) {
		first = filepath.Join(r.cwd, first)
	}
	if st, err := os.Stat(first); err == nil && !st.IsDir() && r.as == "" {
		rel, err := filepath.Rel(root, first)
		if err != nil || strings.HasPrefix(rel, "..") {
			return stop(fmt.Errorf("%s is not in the project ( %s ): it would not be on %s", r.words[0], root, ssh.String("host")))
		}
		spec.File = filepath.ToSlash(rel)
	}
	if filepath.IsAbs(spec.Cwd) {
		spec.Cwd = "" // started from outside the project: from its root, over there
	}
	spec.Cwd = filepath.ToSlash(spec.Cwd)
	loud := !o.quiet
	at := r.placed // ( a command of several runs of entries got there once, for all of them )
	if at == nil {
		var code int
		if at, code = r.reach(spec.Stack, spec.Tags); at == nil {
			return code
		}
	}
	l, h, remoteRoot := at.l, *at.h, at.root
	spec.Root, spec.Outputs, spec.Stack, spec.Keep, spec.Python = remoteRoot, "", spec.Stack[1:], true, ssh.String("python")
	got, err := askLaunch(t, l, map[string]any{"op": "launch", "spec": spec}, o.quiet)
	if errors.Is(err, reach.ErrStopped) {
		return 130
	}
	if err != nil {
		return stop(err)
	}
	var there runinfo.Launched
	if err := reach.DecodeInto(got["launched"], &there); err != nil || there.Note.ID == "" {
		return stop(fmt.Errorf("%s did not say what it started", h.Host))
	}
	if there.Said != "" && !o.quiet {
		t.Say(there.Said)
	}
	from, to := there.Outputs, doc.OutputsDir()
	if o.out != "" { // its directory is elsewhere than under the outputs: named from the root, on both sides
		from, to = there.Note.Project, root
	}
	rel, err := filepath.Rel(from, there.Note.Leaf)
	if err != nil {
		return stop(err)
	}
	note := there.Note
	note.There = &runinfo.FarRun{Name: there.Host, Leaf: there.Note.Leaf, Root: there.Note.Project, Outputs: from,
		LeafRel: filepath.ToSlash(rel), Pull: append(append(zonesOf(doc, "back"), ssh.Strings("fetch")...), o.pull...), Caches: append([]string{downloadsZone}, zonesOf(doc, "cache")...)}
	note.Host, note.Project, note.Batch = &h, root, o.batch
	note.Leaf = filepath.Join(to, filepath.FromSlash(note.There.LeafRel))
	note.Write()
	if o.batch {
		t.Printf("%s  %s\n", note.ID, note.What)
		if loud {
			t.Sayf("  launched on %s: errand attach %s to follow it, errand status to see how it goes\n", there.Host, note.ID)
		}
		return 0
	}
	banner := "ssh " + h.Host
	if h.VM != nil {
		banner = h.Host
	}
	if there.Banner != "" {
		banner += " · " + there.Banner
	}
	if r.leaf != nil {
		*r.leaf = note.Leaf
	}
	v := viewOf(o)
	v.lock = lockTaker(t, r.doc)
	return follow(t, note, v, banner, r.cwd, true)
}
