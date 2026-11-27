package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A Windows, whichever way it is reached, is another machine: what comes after it in a stack runs there. The document
// writes it as two layers ( `Ssh`, then `WindowsBox` or `Windows` ) or one ( `Windows`, a machine booted here ); the
// program reads one -- the `Ssh` that reaches it, with what its Windows is under `windows` -- and, right after it, what
// its commands are given there ( `WindowsTools` ). Everything that asks where a stack leads asks one thing.

// windowsRecipe: what an image is made of beside what its layer declares. Raised when the installation changes.
const windowsRecipe = "go-1"

// GuestPython: the Python every guest has, when its layer names none
const GuestPython = "3.13.16"

func orString(l Layer, name, otherwise string) string {
	if v, said := l[name]; said {
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	return otherwise
}

func orBool(l Layer, name string, otherwise bool) bool {
	if v, ok := l[name].(bool); ok {
		return v
	}
	return otherwise
}

// imageName: an installed image is named by what it was installed from -- an ISO by its path, which is of the machine that
// boots it: the same name whichever machine the command is typed on ( another ISO at that path: `setup --force` )
func imageName(l Layer, root string) string {
	iso := orString(l, "iso", "")
	if iso != "" && !strings.Contains(iso, "://") && !filepath.IsAbs(iso) { // a file of the project: its image is the project's
		iso = filepath.Join(root, iso)
	}
	parts := []any{windowsRecipe, iso, orString(l, "key", ""), l["index"], orString(l, "disk", "64G"),
		orBool(l, "msvc", true), orString(l, "python", GuestPython)}
	for _, p := range l.Strings("provision") {
		if strings.HasSuffix(p, ".ps1") {
			if data, err := os.ReadFile(under(root, p)); err == nil {
				p = string(data)
			}
		}
		parts = append(parts, p)
	}
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return "go-" + hex.EncodeToString(sum[:])[:24]
}

// Images: the names of the images the project's environments boot from
func (d *Document) Images() []string {
	out := []string{}
	for _, e := range d.Envs {
		if len(e.Stack) > 0 {
			w, _ := e.Stack[0]["windows"].(map[string]any)
			if vm, ok := w["vm"].(map[string]any); ok {
				out = append(out, fmt.Sprint(vm["name"]))
			}
		}
	}
	return out
}

func under(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

// folded: a stack as the program reads it
func (d *Document) folded(stack []Layer) []Layer {
	var via, win Layer
	rest := stack
	switch {
	case len(stack) > 1 && stack[0].Type() == "Ssh" && (stack[1].Type() == "WindowsBox" || stack[1].Type() == "Windows"):
		via, win, rest = stack[0], stack[1], stack[2:]
	case len(stack) > 0 && stack[0].Type() == "Windows":
		win, rest = stack[0], stack[1:]
	default:
		return stack
	}
	name := filepath.Base(d.Root)
	out := Layer{"type": "Ssh", "also": win["also"], "fetch": win["fetch"]}
	box, mark := win.Type() == "WindowsBox", ""
	if box {
		out["host"], out["options"], out["cache"], out["root"] = via["host"], via["options"], via["cache"], orString(win, "root", "C:/errand/work/"+name)
		setup := d.setupOf(win)
		b, _ := json.Marshal(setup)
		sum := sha256.Sum256(b)
		mark = "C:/errand/ready/go-" + hex.EncodeToString(sum[:])[:24]
		out["windows"] = map[string]any{"box": true, "setup": setup, "mark": mark}
	} else {
		builds := "errand-outputs/builds"
		if d.Settings.OutputsDir != "" {
			builds = path.Join(filepath.ToSlash(d.Settings.OutputsDir), "builds")
		}
		if b := d.Settings.BuildsDir; b != nil && *b != "" {
			builds = filepath.ToSlash(*b)
		}
		data := orString(win, "data", "32G")
		there, at := d.Root, "windows"
		if via != nil {
			at = "windows on " + via.String("host")
			if r := via.String("root"); r != "" {
				there = r
			}
		}
		root := "C:/errand/" + name
		if data != "" {
			root = "E:/" + name
		}
		image := imageName(win, d.Root)
		cpus := 4
		if n, ok := win["cpus"].(float64); ok {
			cpus = int(n)
		}
		out["host"], out["root"] = at, orString(win, "root", root)
		vm := map[string]any{"root": there, "image": path.Join(builds, "windows", image), "name": image, "cpus": cpus,
			"ram": orString(win, "ram", "8G"), "keep": orString(win, "keep", "5m"), "data": data, "keyed": orString(win, "key", "") != ""}
		w := map[string]any{"vm": vm, "setup": d.setupOf(win)}
		if via != nil {
			w["via"] = map[string]any{"host": via["host"], "options": via["options"], "cache": via["cache"]}
		}
		out["windows"] = w
	}
	tools := Layer{"type": "WindowsTools", "msvc": orBool(win, "msvc", !box), "box": box, "ready": mark}
	return append([]Layer{out, tools}, rest...)
}

// setupOf: what a Windows is set up from, as data: its declaration, and the scripts of the project it names
func (d *Document) setupOf(l Layer) map[string]any {
	out := map[string]any{"recipe": windowsRecipe}
	for k, v := range l {
		if k != "type" && k != "provision" && k != "also" && k != "fetch" && k != "root" {
			out[k] = v
		}
	}
	var steps []any
	for _, p := range l.Strings("provision") {
		step := map[string]any{"line": p}
		if strings.HasSuffix(p, ".ps1") {
			data, err := os.ReadFile(under(d.Root, p))
			if err != nil {
				data = []byte("throw " + fmt.Sprintf("%q", p+": not a file of the project"))
			}
			step = map[string]any{"name": filepath.Base(p), "text": string(data)}
		}
		steps = append(steps, step)
	}
	out["provision"] = steps
	return out
}

// UnmarshalJSON: a document, its stacks read as the program reads them
func (d *Document) UnmarshalJSON(data []byte) error {
	type plain Document
	if err := json.Unmarshal(data, (*plain)(d)); err != nil {
		return err
	}
	if d.DefaultEnv == "" {
		d.DefaultEnv = d.FirstEnv
	}
	for name, e := range d.Envs {
		for i, l := range e.Prepare { // ( a layer of the run's, named by its place in it: before places change )
			if at, ok := l["same"].(float64); ok && int(at) < len(e.Stack) {
				e.Prepare[i] = e.Stack[int(at)]
			}
		}
		e.Stack, e.Prepare = d.folded(e.Stack), d.folded(e.Prepare)
		d.Envs[name] = e
	}
	return nil
}
