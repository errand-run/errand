// Containers ( the guide's "Containers: Apptainer, Docker, Podman", ADR 0004, 0035 ): an image built from a recipe, or
// pulled, and kept current as any artefact is, and the command that enters one.
//
// `Docker` and `Podman` are one thing with another program: the engine is a field, and `Podman` is the layer whose
// default is `podman`, and which asks for no `--user` ( rootless, it already is the caller ). Nothing else tells them
// apart here.
//
// An image is an artefact built **aside**: a build that fails costs nothing ( an Apptainer image is built as
// `<image>.new` and renamed once whole, the one it replaces kept as `<image>.prev`; a Docker tag only moves when a
// build succeeded ), so a build is tried on no more than a suspicion -- an image older than its recipe that errand
// has no record of is rebuilt where any other thing found would be adopted.
//
// An image whose layer says `build_on` is built in another environment of the project, and carried: what this package
// says of it is only what is true wherever it is ( its fingerprint, its stamp, `install` ), and `Builder` is the name of the
// environment a run builds it in, which whoever drives the command goes by ( `command/builton.go` ).

package layer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/system"
)

// isEngine: `Docker` and `Podman`, the layers that enter an image through a container engine
func isEngine(typ string) bool { return typ == "Docker" || typ == "Podman" }

func (a Artefact) isContainer() bool {
	return a.Layer.Type() == "Apptainer" || isEngine(a.Layer.Type())
}

// engine: the program that runs a `Docker` or `Podman` layer's image
func engine(a Artefact) string {
	if e := a.Layer.String("engine"); e != "" {
		return e
	}
	return strings.ToLower(a.Layer.Type())
}

// apptainerExe: what a site calls apptainer -- `singularity`, at some
func apptainerExe(a Artefact) string {
	if e := a.Layer.String("exe"); e != "" {
		return e
	}
	return "apptainer"
}

// containerName: what the layer is called, as a person reads it ( `apptainer:cuda.sif`, `docker:myproj:1` ); an image
// in errand's cache by its recipe ( `apptainer:cuda.def` )
func containerName(l project.Layer) string {
	if l.Type() == "Apptainer" {
		if l.String("image") == "" {
			return "apptainer:" + filepath.Base(l.String("recipe"))
		}
		return "apptainer:" + filepath.Base(l.String("image"))
	}
	e := l.String("engine")
	if e == "" {
		e = strings.ToLower(l.Type())
	}
	return e + ":" + l.String("image")
}

// ImageOf: where an Apptainer image is, from the project's root at `root` -- the one the layer names, or, built from a recipe
// and not named, in errand's directory of this machine, named by what it is made of ( ADR 0044 ): shared by the projects
// that build the same, never changed once built
func ImageOf(l project.Layer, root string) string {
	if image := l.String("image"); image != "" || l.Type() != "Apptainer" {
		return image
	}
	recipe := l.String("recipe")
	if recipe == "" || strings.Contains(recipe, "://") {
		return ""
	}
	stem := strings.TrimSuffix(filepath.Base(recipe), filepath.Ext(recipe))
	return filepath.Join(ImagesStore(), stem+"-"+Artefact{Layer: l}.Fingerprint(Context{Root: root})+".sif")
}

// ImagesStore: where this machine keeps the images errand builds that no layer names
func ImagesStore() string { return filepath.Join(system.Cache(), "images") }

// InStore: whether an image is one of errand's cache, which only errand writes
func InStore(image string) bool {
	rel, err := filepath.Rel(ImagesStore(), image)
	return err == nil && !strings.HasPrefix(rel, "..") && filepath.IsAbs(image)
}

// ImagePath: where the artefact's image is, for the project at `root`
func (a Artefact) ImagePath(root string) string { return ImageOf(a.Layer, root) }

// Aside: what a build cannot harm, and so is attempted on a suspicion: an image made from a recipe
func (a Artefact) Aside() bool { return a.isContainer() && a.Layer.String("recipe") != "" }

// containerInputs: the recipe, when it is a file of the project ( a URI such as `docker://…` is the recipe itself ), and
// what it copies from the project into the image ( ADR 0044 ): a file `%files` or `COPY` takes changes the image as much as
// a line of the recipe
func (a Artefact) containerInputs(root string) []string {
	r := a.Layer.String("recipe")
	if r == "" || strings.Contains(r, "://") {
		return nil
	}
	return append([]string{r}, recipeSources(root, r, a.Layer.Type() == "Apptainer")...)
}

// recipeSources: the paths of the project a recipe copies into the image, from the project's root -- `%files` of an
// Apptainer definition ( not `%files from <stage>` ), `COPY` and `ADD` of a Dockerfile ( not `--from=` ). A source that
// is a URL or a pattern is left out: the first is the recipe's to fetch, the second is not one path.
func recipeSources(root, recipe string, apptainer bool) []string {
	data, err := os.ReadFile(filepath.Join(root, recipe))
	if err != nil {
		return nil
	}
	var out []string
	add := func(p string) {
		p = strings.Trim(p, `"'`)
		if p == "" || strings.Contains(p, "://") || strings.ContainsAny(p, "*?[$") || filepath.IsAbs(p) {
			return
		}
		out = append(out, filepath.ToSlash(filepath.Clean(p)))
	}
	inFiles := false
	lines := strings.Split(strings.ReplaceAll(string(data), "\\\n", " "), "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if apptainer {
			if strings.HasPrefix(t, "%") {
				f := strings.Fields(t)
				inFiles = f[0] == "%files" && len(f) == 1
				continue
			}
			if inFiles {
				add(strings.Fields(t)[0])
			}
			continue
		}
		f := strings.Fields(t)
		if op := strings.ToUpper(f[0]); (op == "COPY" || op == "ADD") && len(f) > 2 {
			var words []string
			for _, w := range f[1:] {
				if strings.HasPrefix(w, "--from=") {
					words = nil
					break
				}
				if !strings.HasPrefix(w, "--") {
					words = append(words, w)
				}
			}
			if strings.HasPrefix(strings.Join(words, " "), "[") { // the JSON form
				var arr []string
				if json.Unmarshal([]byte(strings.Join(words, " ")), &arr) == nil {
					words = arr
				}
			}
			for i := 0; i+1 < len(words); i++ { // the last one is where it goes
				add(words[i])
			}
		}
	}
	return out
}

// containerFields: what an image is made from, besides its recipe's contents. How it is entered ( `flags`, `mounts` )
// and where it is built ( `scratch`, `fakeroot` ) do not change what comes out, and are not here.
func (a Artefact) containerFields() []string {
	if a.Layer.Type() == "Apptainer" { // ( not where it goes: an image moved is the same image )
		return []string{"type", "recipe", "pip", "build_flags"}
	}
	return []string{"type", "image", "recipe", "pip", "engine"}
}

// Needs: what the place must have for this to be built, as a line of shell whose exit code says it, and what is
// lacking when it fails -- checked before anything is built
func (a Artefact) Needs() [][2]string {
	if !a.isContainer() {
		return nil
	}
	if a.Layer.Type() == "Apptainer" {
		exe := apptainerExe(a)
		return [][2]string{{"command -v " + Quote(exe), exe + ", not found"}}
	}
	e := engine(a)
	return [][2]string{
		{"command -v " + Quote(e), e + ", not found"},
		{sh(e, "info"), "a running " + e + " ( `" + e + " info` fails )"},
	}
}

// BuiltOn: where the image is built, when it is built elsewhere and carried to where it is used -- `build_on`: the name of
// one of the project's environments, or the layers of one written in place ( a list, or a single layer ). A place that has
// the internet the recipe wants, and the architecture. -> the environment's name, or the layers; ok when it is said.
func (a Artefact) BuiltOn() (name string, stack []project.Layer, ok bool) {
	if a.Layer.Type() != "Apptainer" {
		return "", nil, false
	}
	switch on := a.Layer["build_on"].(type) {
	case string:
		return on, nil, on != ""
	case []any:
		for _, item := range on {
			l, isLayer := layerOf(item)
			if !isLayer {
				return "", nil, false
			}
			stack = append(stack, l)
		}
		return "", stack, true
	case map[string]any:
		l, isLayer := layerOf(on)
		return "", []project.Layer{l}, isLayer
	}
	return "", nil, false
}

// layerOf: a layer as a document holds one inside another -- `{ "layer": { "type": … } }`, or the layer itself
func layerOf(v any) (project.Layer, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	if inner, wrapped := m["layer"].(map[string]any); wrapped {
		m = inner
	}
	if _, typed := m["type"].(string); !typed {
		return nil, false
	}
	return project.Layer(m), true
}

// Builder: what the environment the image is built in is called, as a person reads it -- "" when it is not built elsewhere
func (a Artefact) Builder() string {
	name, stack, ok := a.BuiltOn()
	switch {
	case !ok:
		return ""
	case name != "":
		return name
	}
	return Describe(stack)
}

// Refusal: why this program cannot build the image when it must, and what to do instead -- "" when it can
func (a Artefact) Refusal() string {
	if _, _, ok := a.BuiltOn(); a.Layer.Type() == "Apptainer" && a.Layer["build_on"] != nil && !ok {
		return "`build_on` is the name of an environment of the project, or the layers of one: " +
			"`build_on = \"builder\"`, or `build_on = [ errand.Ssh( … ) ]`"
	}
	return ""
}

// Built: the layer as the environment that builds it is asked to make it -- the image alone: what is done once it is
// where it is used ( `install` ) is done there, and where it is built is not a reason to build it again
func (a Artefact) Built() project.Layer {
	out := project.Layer{}
	for k, v := range a.Layer {
		if k != "build_on" && k != "install" && k != "run_from" {
			out[k] = v
		}
	}
	return out
}

// Installs: the lines run where the image is used, once it is there
func (a Artefact) Installs(root string) []string {
	if install := a.Layer.String("install"); install != "" {
		return []string{strings.ReplaceAll(install, "{image}", Quote(a.ImagePath(root)))}
	}
	return nil
}

// Shows: whether what a build says is shown as it says it, and not kept for when it fails -- an image takes minutes,
// and a build that says nothing for as long is a build that looks stuck
func (a Artefact) Shows() bool { return a.isContainer() }

// Abandon: a line that removes what a failed build left half made
func (a Artefact) Abandon(root string) string {
	if a.Layer.Type() != "Apptainer" {
		return ""
	}
	image := a.ImagePath(root)
	return sh("rm", "-f", image+".new", image+".new.pip", image+".new.pip.def")
}

// Made: when the artefact was last written, as its stamp says
func (a Artefact) Made(stamp string) (float64, bool) {
	fields := strings.Fields(stamp)
	if isEngine(a.Layer.Type()) { // `<id> <created>`
		if len(fields) < 2 {
			return 0, false
		}
		when, err := time.Parse(time.RFC3339Nano, fields[len(fields)-1])
		return float64(when.Unix()), err == nil
	}
	if len(fields) == 0 {
		return 0, false
	}
	var when float64
	_, err := fmt.Sscan(fields[0], &when)
	return when, err == nil
}

func (a Artefact) containerProbe(root string) string {
	if a.Layer.Type() == "Apptainer" {
		return sh("test", "-e", a.ImagePath(root))
	}
	return sh(engine(a), "image", "inspect", a.Layer.String("image"))
}

func (a Artefact) containerStamp(root string) string {
	image := a.ImagePath(root)
	if a.Layer.Type() == "Apptainer" {
		q := Quote(image)
		return "stat -c '%Y %s' " + q + " || stat -f '%m %z' " + q
	}
	return sh(engine(a), "image", "inspect", "-f", "{{.Id}} {{.Created}}", image)
}

// replaceLine: `new` put in the place of `image`, the one it replaces kept once -- a recipe that downloads what it
// likes cannot always rebuild it, and a build that succeeds can still give something that does not work
func replaceLine(new, image string) string {
	return "if test -e " + Quote(image) + "; then mv -f " + Quote(image) + " " + Quote(image+".prev") + "; fi; mv -f " + Quote(new) + " " + Quote(image)
}

// containerSteps: what makes the image, or brings it where it is -- each a line of shell, run from the project's root
func (a Artefact) containerSteps(root string) []string {
	l := a.Layer
	image, recipe, pip := a.ImagePath(root), l.String("recipe"), l.Strings("pip")
	if a.Layer.Type() != "Apptainer" {
		e := engine(a)
		if recipe == "" { // nobody builds it here: fetched
			return []string{sh(e, "pull", image)}
		}
		steps := []string{sh(e, "build", "-t", image, "-f", recipe, ".")}
		if len(pip) > 0 { // a layer on top of what was built, under the same tag
			steps = append(steps, "printf '%s\\n' "+Quote("FROM "+image)+" "+Quote("RUN python -m pip install "+sh(pip...))+" | "+
				sh(e, "build", "-t", image, "-"))
		}
		return steps
	}
	if recipe == "" {
		return nil // an image somebody else builds: nothing to say about it
	}
	if a.Builder() != "" {
		return a.Installs(root) // built elsewhere, and carried: what is left to do here is what is done with it
	}
	exe := apptainerExe(a)
	target := image + ".new"
	// the directory the image goes in, and the one apptainer unpacks its layers in: both must exist, and a build that
	// runs out of room in either dies the same way
	prefix := "mkdir -p " + Quote(filepath.Dir(image))
	build := func(out, from string) string {
		argv := []string{exe, "build", "--force"}
		if fake, _ := l["fakeroot"].(bool); fake {
			argv = append(argv, "--fakeroot")
		}
		argv = append(append(argv, l.Strings("build_flags")...), out, from)
		line := sh(argv...)
		if scratch := l.String("scratch"); scratch != "" {
			line = "mkdir -p " + Quote(scratch) + " && APPTAINER_TMPDIR=" + Quote(scratch) + " APPTAINER_CACHEDIR=" + Quote(scratch) + " " + line
		}
		return prefix + " && " + line
	}
	steps := []string{build(target, recipe)}
	if len(pip) > 0 {
		// an image is read-only: what is installed into it is a layer made from it, by a definition of its own
		def := target + ".pip.def"
		steps = append(steps,
			"printf '%s\\n' "+Quote("Bootstrap: localimage")+" "+Quote("From: "+target)+" '' '%post' "+Quote("    python -m pip install "+sh(pip...))+" > "+Quote(def)+
				" && "+build(target+".pip", def)+" && mv -f "+Quote(target+".pip")+" "+Quote(target)+" && rm -f "+Quote(def))
	}
	steps = append(steps, replaceLine(target, image))
	return append(steps, a.Installs(root)...) // where it runs, for a site that only executes images from a place of its own
}

// kept: the paths of an image a project's push must leave alone, from its root -- an image built where it is used is
// no part of a copy. Only for an image errand builds: one somebody else made travels as any file of the project.
func (a Artefact) kept() []string {
	image := a.Layer.String("image")
	if !a.Aside() || a.Layer.Type() != "Apptainer" || image == "" || filepath.IsAbs(image) {
		return nil
	}
	return []string{image, image + ".new", image + ".prev", image + ".part"}
}

// engineUser: what a container engine is told of who runs: the caller's own, unless the layer says otherwise. A run
// writes its results into the project, and a daemon-backed container writes as root unless told otherwise: the next
// run outside it could not clear them. `Podman`, rootless, already is the caller.
func engineUser(l project.Layer) []string {
	user, said := l["user"]
	switch {
	case !said:
		if l.Type() == "Podman" {
			return nil
		}
		user = "caller"
	case user == nil:
		return nil
	}
	if s := fmt.Sprint(user); s != "caller" {
		return []string{"--user", s}
	}
	return []string{"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
}

// enterEngine: `docker run` -- the project at its own path and the place to start from, what must be seen from inside
// at the same paths, the layer's mounts, and what errand's agent inside needs of the surroundings ( an engine passes
// none of its own: the program that runs in the image has the image's )
func enterEngine(l project.Layer, ctx Context, binds []string, argv []string) Command {
	e := l.String("engine")
	if e == "" {
		e = strings.ToLower(l.Type())
	}
	enter := append([]string{e, "run", "--rm"}, engineUser(l)...)
	if ctx.Hostname != "" {
		enter = append(enter, "--hostname", ctx.Hostname)
	}
	enter = append(enter, l.Strings("flags")...)
	seen := map[string]bool{}
	mount := func(spec string) {
		if !seen[spec] {
			seen[spec] = true
			enter = append(enter, "-v", spec)
		}
	}
	if mounts, ok := l["mounts"].(map[string]any); ok {
		names := make([]string, 0, len(mounts))
		for src := range mounts {
			names = append(names, src)
		}
		sort.Strings(names)
		for _, src := range names {
			from := src
			if !filepath.IsAbs(from) {
				from = filepath.Join(ctx.Root, from)
			}
			mount(from + ":" + fmt.Sprint(mounts[src]))
		}
	}
	for _, b := range binds {
		if !strings.Contains(b, ":") {
			b += ":" + b
		}
		mount(b)
	}
	mount(ctx.Root + ":" + ctx.Root)
	environ := ctx.Environ
	if environ == nil {
		environ = os.Environ()
	}
	for _, kv := range environ {
		if strings.HasPrefix(kv, "ERRAND") {
			enter = append(enter, "-e", kv)
		}
	}
	enter = append(enter, "-w", ctx.Root, l.String("image"))
	return Command{append(enter, argv...), map[string]string{}}
}
