package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/errand-run/errand/internal/project"
)

func TestAVenvOnWindows(t *testing.T) {
	OnWindows = true
	defer func() { OnWindows = false }()
	ctx := Context{Root: t.TempDir()}
	venv := Artefacts([]project.Layer{{"type": "Venv", "python": ".venv/bin/python", "create": true, "pip": []any{"numpy"}}})[0]
	uv := Artefacts([]project.Layer{{"type": "Uv", "path": "uvenv", "python": "3.12", "pip": []any{"a b"}}})[0]
	if got := strings.Join(venv.Steps(ctx), " ; "); got != "python3 -m venv .venv ; .venv/Scripts/python.exe -m pip install numpy" {
		t.Errorf("a venv: %s", got)
	}
	if got := strings.Join(uv.Steps(ctx), " ; "); got != "uv venv uvenv --python 3.12 ; uv pip install --python uvenv/Scripts/python.exe 'a b'" {
		t.Errorf("uv: %s", got)
	}
	if venv.Present(ctx) {
		t.Error("there before it is made")
	}
	_ = os.MkdirAll(filepath.Join(ctx.Root, ".venv", "Scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(ctx.Root, ".venv", "Scripts", "python.exe"), nil, 0o755)
	if !venv.Present(ctx) {
		t.Error("not there once it is made")
	}
	got, err := Wrap([]project.Layer{venv.Layer}, Command{Argv: []string{"python", "x.py"}}, ctx)
	if err != nil || got.Argv[0] != filepath.ToSlash(filepath.Join(ctx.Root, ".venv/Scripts/python.exe")) {
		t.Errorf("what runs: %v %v", got.Argv, err)
	}
	if kept := Kept([]project.Layer{venv.Layer, uv.Layer}); strings.Join(kept, " ") != ".venv uvenv" {
		t.Errorf("kept: %v", kept)
	}
}
