// An image built in another environment, and carried ( the guide's "A cluster without internet", ADR 0004, 0006, 0035 ):
// `build_on = "builder"` names an environment of the project -- a machine that has the internet a recipe wants, and
// the architecture --, and the image is made there, then brought to the copy of the project where it is used.
//
// It is a step of preparing, which appears when a stack holds an image that is built elsewhere. **The build is a run in the
// builder** -- `sh -c` of the lines that make the image, through the builder's own layers ( an allocation, a container,
// its variables ), its record and its place in the queue like any run --, and **whoever drives carries the image**, as for
// a file a run cannot download: the machine that holds the image has no way to the builder, and the two need not reach each
// other.
//
// Before a run, and for `errand setup`, it asks the agent that holds the image what of the stack is wanted ( the record
// there says whether the recipe or the image moved: a recipe that changed, an image that is not there, an image changed
// outside errand while its recipe changed too -- which is said, and not replaced unless `--force` says so ); asks the
// builder's agent the same of the image it makes, so that what is current there is carried and not made again; runs the
// build when it is wanted; takes the image from the builder's copy and puts it into this one's ( the one it replaces kept
// as `<image>.prev` ); and tells the agent it is there: the agent runs what is done with it ( `install` ) and writes the
// record, so that from then on it is an image like any other -- adopted, kept current, stale when its recipe changes.

package command

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
)

// builtElsewhere: whether a stack holds an image that is built in another environment
func builtElsewhere(stack []project.Layer) bool {
	for _, a := range layer.Artefacts(stack) {
		if a.Builder() != "" {
			return true
		}
	}
	return false
}

// carryImages: what a stack holds that is built in another environment, and is wanted now -- made there and put where
// the agent `l` holds the project's copy ( at `dest`: a machine reached by ssh, or nil for this one ). `req` says where
// that is: its `root`, the `tags`, the surroundings. `o`: the command it is a step of.
func carryImages(t *reach.Term, doc *project.Document, dest *reach.HostSpec, l *reach.Link, req map[string]any, stack []project.Layer, force bool, o runOptions) error {
	if !builtElsewhere(stack) {
		return nil
	}
	ask := map[string]any{"op": "elsewhere", "stack": stack, "force": force}
	for _, k := range []string{"root", "outputs", "tags", "environ"} {
		if v, ok := req[k]; ok {
			ask[k] = v
		}
	}
	got, err := l.Ask(ask)
	if err != nil {
		return err
	}
	var wanted []agent.CarriedImage
	if err := reach.DecodeInto(got["wanted"], &wanted); err != nil {
		return err
	}
	slugs := []string{}
	from := ""
	for _, w := range wanted {
		if err := carryOne(t, doc, dest, l, req, stack, w, force, o); err != nil {
			return err
		}
		slugs, from = append(slugs, w.Slug), w.Builder
	}
	if len(slugs) == 0 {
		return nil
	}
	ask["op"], ask["slugs"], ask["from"] = "carried", slugs, from
	got, err = l.Ask(ask)
	if err != nil {
		return err
	}
	if !o.quiet {
		for _, line := range kit.Strs(got["said"]) {
			t.Say(line)
		}
	}
	return nil
}

// carryOne: one image, made in its builder when the builder does not have it as it should be, and brought here
func carryOne(t *reach.Term, doc *project.Document, dest *reach.HostSpec, l *reach.Link, req map[string]any, stack []project.Layer, w agent.CarriedImage, force bool, o runOptions) error {
	var art *layer.Artefact
	for _, a := range layer.Artefacts(stack) {
		if a.Slug() == w.Slug {
			found := a
			art = &found
		}
	}
	if art == nil {
		return fmt.Errorf("%s: it is not in the environment's stack", w.Name)
	}
	builder, builderName, err := builderOf(doc, *art)
	if err != nil {
		return fmt.Errorf("%s: %w", w.Name, err)
	}
	say := func(text string) {
		if !o.quiet {
			t.Say(text)
		}
	}
	mine := layer.Artefact{Layer: art.Built(), Name: art.Name} // the image as the builder makes it: no `install`, no `build_on`
	// where the builder is: its machine's agent, and the project's copy there ( pushed first: the recipe is in it )
	lb, _, where, err := placeOf(t, doc, builderName, builder, true, o.force)
	if err != nil {
		return fmt.Errorf("%s: the environment %s, where it is built: %w", w.Name, w.Builder, err)
	}
	// is it as it should be there already? a second place that wants the same image is a copy, not a build
	ask := map[string]any{"op": "elsewhere", "stack": []project.Layer{mine.Layer}, "force": force, "all": true}
	for _, k := range []string{"root", "outputs", "tags", "environ"} {
		if v, ok := where[k]; ok {
			ask[k] = v
		}
	}
	got, err := lb.Ask(ask)
	if err != nil {
		return fmt.Errorf("%s: asked of %s: %w", w.Name, w.Builder, err)
	}
	var builds []agent.CarriedImage
	_ = reach.DecodeInto(got["wanted"], &builds)
	if len(builds) > 0 {
		say(fmt.Sprintf("  %s is built in %s", w.Name, w.Builder))
		if err := buildIn(t, doc, w, builderName, builder, mine, o); err != nil {
			return err
		}
		ask["op"], ask["slugs"], ask["from"] = "carried", []string{w.Slug}, ""
		if _, err := lb.Ask(ask); err != nil { // the record there: what it was made from, and when
			return fmt.Errorf("%s: %w", w.Name, err)
		}
	}
	// carried: from the builder's copy, through this machine, into the copy where it is used
	builderRoot := kit.Text(where["root"])
	building := len(builder.Stack) > 0 && builder.Stack[0].Type() == "Ssh"
	stage := doc.Root // a builder on this machine: its copy is the project
	if building {
		stage = filepath.Join(doc.OutputsDir(), "state", "carry", w.Slug)
		_ = os.RemoveAll(stage)
		defer os.RemoveAll(stage)
		host := reach.HostOf(builder.Stack[0]).Host
		done := t.Working("work", "↓ pull "+w.Image+" ← "+host)
		_, err := filesync.Pull(lb, builderRoot, stage, []string{w.Image}, nil, false, host, t.Say)
		done()
		if err != nil {
			return fmt.Errorf("%s could not be brought from %s: %w", w.Name, host, err)
		}
	}
	file := filepath.Join(stage, filepath.FromSlash(w.Image))
	if st, err := os.Stat(file); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%s: %s did not make %s", w.Name, w.Builder, w.Image)
	}
	switch {
	case dest != nil:
		done := t.Working("work", "↑ send "+w.Image+" → "+dest.Host)
		err := filesync.SendInto(l, stage, kit.Text(req["root"]), []string{w.Image}, true, "", nil)
		done()
		if err != nil {
			return fmt.Errorf("%s could not be sent to %s: %w", w.Name, dest.Host, err)
		}
		say(fmt.Sprintf("  %s: carried to %s", w.Image, dest.Host))
	case building:
		if err := moveInto(file, filepath.Join(doc.Root, filepath.FromSlash(w.Image))); err != nil {
			return fmt.Errorf("%s could not be put in place: %w", w.Name, err)
		}
		say(fmt.Sprintf("  %s: carried here", w.Image))
	}
	return nil
}

// builderOf: the environment an image is built in -- one of the project's, by its name, or the layers written in the image's
// own: an environment that has no name, and is called what it is made of
func builderOf(doc *project.Document, a layer.Artefact) (project.Env, string, error) {
	name, stack, _ := a.BuiltOn()
	if name == "" {
		return project.Env{Stack: stack}, "build_on", nil
	}
	env, ok := doc.Envs[name]
	if !ok {
		return env, name, fmt.Errorf("build_on names the environment %q, which this project does not declare", name)
	}
	return env, name, nil
}

// buildIn: the lines that make an image, run in an environment as any command is -- through its layers, with its
// variables, in its queue, recorded --, and said to this terminal as a run is. What it prints goes to the error
// output, and it reads nothing: the command this is a step of owns both.
func buildIn(t *reach.Term, doc *project.Document, w agent.CarriedImage, envName string, env project.Env, a layer.Artefact, o runOptions) error {
	lines := a.Steps(layer.Context{})
	if len(lines) == 0 {
		return fmt.Errorf("%s has no recipe: it cannot be built in %s", w.Name, w.Builder)
	}
	for i, line := range lines { // each a whole: a step that has a `;` of its own does not end the chain
		lines[i] = "{ " + line + "; }"
	}
	script := strings.Join(lines, " && ")
	if undo := a.Abandon(); undo != "" { // what a failed build leaves half made goes
		script = "( " + script + " ) || { " + undo + "; exit 1; }"
	}
	sub := runOptions{flags: map[string][]string{}, quiet: o.quiet, plain: o.plain, verbose: o.verbose, label: "build " + w.Image}
	r := aRun{t: t, o: sub, doc: doc, cwd: doc.Root, when: time.Now(), envName: envName, env: env, tags: env.Tags,
		words: []string{"sh", "-c", script}, only: true}
	emit, stdin := t.Emit, t.Stdin
	closed := make(chan []byte)
	close(closed)
	t.Emit = func(kind byte, payload []byte) {
		if kind == kit.OUT {
			kind = kit.ERR
		}
		emit(kind, payload)
	}
	t.Stdin = closed
	defer func() { t.Emit, t.Stdin = emit, stdin }()
	switch code := r.run(); code {
	case 0:
		return nil
	case 130:
		return reach.ErrStopped
	default:
		return fmt.Errorf("%s was not built in %s ( its run ended with %d; what it said is above )", w.Name, w.Builder, code)
	}
}

// moveInto: `from` put at `to`, the file it replaces kept as `<to>.prev` -- written beside it and renamed when whole,
// as a copy that was interrupted is never taken for the image
func moveInto(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	part := to + ".part"
	if err := os.Rename(from, part); err != nil { // another disk: copied
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(part)
		if err != nil {
			return err
		}
		if _, err = io.Copy(out, in); err != nil {
			out.Close()
			_ = os.Remove(part)
			return err
		}
		if err = out.Close(); err != nil {
			_ = os.Remove(part)
			return err
		}
	}
	if st, err := os.Lstat(to); err == nil && st.Mode().IsRegular() {
		_ = os.Rename(to, to+".prev")
	}
	if err := os.Rename(part, to); err != nil {
		return errors.New(err.Error())
	}
	return nil
}
