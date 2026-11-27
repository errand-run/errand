package layer

import (
	"path/filepath"
	"testing"

	"github.com/errand-run/errand/internal/project"
)

// An interpreter named as for any system is where Windows has it; a venv of errand's is in its store, entered by its
// interpreter
func TestAVenvOnWindows(t *testing.T) {
	OnWindows = true
	defer func() { OnWindows = false }()
	ctx := Context{Root: t.TempDir()}
	got, err := Wrap([]project.Layer{{"type": "Venv", "python": ".venv/bin/python"}}, Command{Argv: []string{"python", "x.py"}}, ctx)
	if err != nil || got.Argv[0] != filepath.ToSlash(filepath.Join(ctx.Root, ".venv/Scripts/python.exe")) {
		t.Errorf("what runs: %v %v", got.Argv, err)
	}
	if p := VenvPython(`C:\s\uv\x`); p != filepath.Join(`C:\s\uv\x`, "Scripts", "python.exe") {
		t.Errorf("a venv's python: %s", p)
	}
	if len(Artefacts([]project.Layer{{"type": "Venv", "python": "py"}})) != 0 || len(Artefacts([]project.Layer{{"type": "Uv"}})) != 1 {
		t.Error("a Venv names an interpreter; a Uv is errand's")
	}
	if _, err := Wrap([]project.Layer{{"type": "Uv"}}, Command{Argv: []string{"python"}}, ctx); err == nil {
		t.Error("a venv of errand's not built: said")
	}
}
