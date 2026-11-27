package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/errand-run/errand/internal/project"
)

// What an image is made from: its recipe, and what the recipe copies from the project ( ADR 0044 )
func TestAnImageIsMadeOfWhatItCopies(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		_ = os.WriteFile(filepath.Join(root, rel), []byte(text), 0o644)
	}
	write("c/main.def", "Bootstrap: docker\nFrom: ubuntu:24.04\n\n%files\n    scripts/setup.sh /opt/setup.sh\n    conf /etc/conf\n\n%files from build\n    /out /out\n\n%post\n    sh /opt/setup.sh\n")
	write("scripts/setup.sh", "echo one\n")
	write("conf/a.toml", "a = 1\n")
	write("Dockerfile", "FROM python:3.12\nCOPY --chown=1000 requirements.txt /r.txt\nCOPY --from=build /x /x\nADD [\"src\", \"/src\"]\nCOPY https://x/y /y\n")
	write("requirements.txt", "numpy\n")
	write("src/m.py", "x = 1\n")
	app := Artefact{Layer: project.Layer{"type": "Apptainer", "image": "c/main.sif", "recipe": "c/main.def"}}
	if got := strings.Join(app.Inputs(root), " "); got != "c/main.def scripts/setup.sh conf" {
		t.Errorf("apptainer: %s", got)
	}
	dock := Artefact{Layer: project.Layer{"type": "Docker", "image": "x:1", "recipe": "Dockerfile"}}
	if got := strings.Join(dock.Inputs(root), " "); got != "Dockerfile requirements.txt src" {
		t.Errorf("docker: %s", got)
	}
	ctx := Context{Root: root}
	before := app.Fingerprint(ctx)
	write("conf/b.toml", "b = 2\n")
	if app.Fingerprint(ctx) == before {
		t.Errorf("a file in a directory `%%files` copies changed nothing")
	}
	before = dock.Fingerprint(ctx)
	write("src/m.py", "x = 2\n")
	if dock.Fingerprint(ctx) == before {
		t.Error("a file COPY takes changed nothing")
	}
}
