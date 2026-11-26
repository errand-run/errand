package system

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstances(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ERRAND_CACHE_DIR", base)
	t.Setenv("ERRAND_INSTANCE", "")
	if Cache() != filepath.Join(base, "main") || Instance() != "main" || Usual() {
		t.Errorf("main: %s", Cache())
	}
	t.Setenv("ERRAND_INSTANCE", "try")
	if dir := Cache(); dir != filepath.Join(base, "try") || !exists(filepath.Join(dir, instanceMark)) {
		t.Errorf("try: %s", dir)
	}
	for _, bad := range []string{"Try", "bin", "place", "a/b", "-x"} {
		t.Setenv("ERRAND_INSTANCE", bad)
		if Instance() != "main" {
			t.Errorf("%q taken", bad)
		}
	}
	if s := CacheShell(`"$HOME/.cache/errand"`); !strings.Contains(s, `c="$b/main"`) || !strings.Contains(s, `[ -e "$b/place" ]`) {
		t.Errorf("%s", s)
	}
	t.Setenv("ERRAND_INSTANCE", "try")
	if s := CacheShell(`"$x"`); s != `b="$x"; c="$b/try"; ` {
		t.Errorf("%s", s)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestTheErrandOfBeforeMovesIntoMain(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ERRAND_CACHE_DIR", base)
	t.Setenv("ERRAND_INSTANCE", "")
	for _, f := range []string{"dir.id", "page/said.json", "place/m/abc/version", "bin/abc/errand"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(base, f)), 0o755)
		_ = os.WriteFile(filepath.Join(base, f), []byte("{}"), 0o644)
	}
	_ = os.Symlink("abc/errand", filepath.Join(base, "bin", "errand"))
	_ = os.MkdirAll(filepath.Join(base, "try"), 0o755) // another instance stays where it is
	_ = os.WriteFile(filepath.Join(base, "try", instanceMark), []byte("try\n"), 0o644)

	// an agent of it runs: it stays where it is, for now
	b, _ := json.Marshal(map[string]any{"pid": os.Getppid()})
	_ = os.WriteFile(filepath.Join(base, "place", "m", "abc", "version"), b, 0o644)
	if Cache() != base || exists(filepath.Join(base, "main")) {
		t.Fatalf("moved while an agent runs: %s", Cache())
	}
	_ = os.WriteFile(filepath.Join(base, "place", "m", "abc", "version"), []byte(`{"pid": 0}`), 0o644)
	if Cache() != filepath.Join(base, "main") {
		t.Fatalf("not moved: %s", Cache())
	}
	for _, f := range []string{"main/dir.id", "main/page/said.json", "main/bin/abc/errand", "try/" + instanceMark} {
		if !exists(filepath.Join(base, f)) {
			t.Errorf("%s is not there", f)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "bin")); err == nil {
		t.Errorf("nothing beside the instances")
	}
	if exists(filepath.Join(base, "dir.id")) || exists(filepath.Join(base, "place")) {
		t.Errorf("left behind")
	}
}
