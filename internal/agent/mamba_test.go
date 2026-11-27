package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
)

// The store keeps what a project's lock holds, and lets go of the rest once unused
func TestTheStoreIsTidied(t *testing.T) {
	t.Setenv("ERRAND_CACHE_DIR", t.TempDir())
	t.Setenv("ERRAND_INSTANCE", "")
	root := t.TempDir()
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	fresh := now.Add(-2 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	made := func(dir, used string, replaced bool) string {
		prefix := filepath.Join(mambaDir(), dir)
		_ = os.MkdirAll(filepath.Join(prefix, "conda-meta"), 0o755)
		_ = writeStore(storeRecord{ID: dir, Dir: dir, Used: used, Replaced: replaced})
		return prefix
	}
	kept := made("declared", old, false)
	unusedLong := made("gone", old, false)
	unusedLately := made("recent", fresh, false)
	replaced := made("declared-2", fresh, true)
	// a project whose lock holds `declared`
	l := project.Layer{"type": "Micromamba", "packages": []any{"numpy"}}
	lock := &layer.Lock{Envs: map[string]*layer.LockedDecl{}}
	lock.Add("d", "micromamba", layer.Resolution{ID: "declared"})
	if err := lock.Write(root); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(layer.RefPath(root, l)), 0o755)
	_ = kit.WriteRecord(layer.RefPath(root, l), layer.MambaRef{Root: root, Declaration: "d", ID: "declared", Prefix: kept, Seen: old})

	// images of the cache: the one the project's recipe makes stays, an old one goes
	_ = os.MkdirAll(filepath.Join(root, "c"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "c", "a.def"), []byte("Bootstrap: docker\n"), 0o644)
	app := project.Layer{"type": "Apptainer", "recipe": "c/a.def"}
	declaredImage := layer.ImageOf(app, root)
	_ = os.MkdirAll(layer.ImagesStore(), 0o755)
	oldImage := filepath.Join(layer.ImagesStore(), "a-0123.sif")
	for _, img := range []string{declaredImage, oldImage} {
		_ = os.WriteFile(img, []byte("sif"), 0o644)
		_ = os.WriteFile(img+".used", nil, 0o644)
		long := now.Add(-40 * 24 * time.Hour)
		_ = os.Chtimes(img+".used", long, long)
	}
	notedImage(root, app, declaredImage)
	_ = os.Chtimes(declaredImage+".used", now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour))

	gone := PurgeMambaStore(now, false, false, nil)
	if !kit.Exists(declaredImage) || kit.Exists(oldImage) {
		t.Errorf("images: the declared one there %v, the old one there %v", kit.Exists(declaredImage), kit.Exists(oldImage))
	}
	if len(gone) != 3 {
		t.Errorf("what went: %v", gone)
	}
	for p, want := range map[string]bool{kept: true, unusedLong: false, unusedLately: true, replaced: false} {
		if kit.IsDir(p) != want {
			t.Errorf("%s: there %v", filepath.Base(p), !want)
		}
	}
	// on a shared machine, a week is enough
	if gone := PurgeMambaStore(now.Add(6*24*time.Hour), true, false, nil); len(gone) != 1 || kit.IsDir(unusedLately) {
		t.Errorf("shared: %v", gone)
	}
	// the project gone, its environment is no longer declared
	_ = os.RemoveAll(root)
	if gone := PurgeMambaStore(now, false, false, nil); len(gone) != 2 || kit.IsDir(kept) || kit.Exists(declaredImage) {
		t.Errorf("a project that is gone: %v", gone)
	}
}
