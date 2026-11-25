package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/project"
)

// BuildDirIn: `errand build-dir NAME` inside a run: the run knows where it builds, nobody is asked -> its path, made
func BuildDirIn(name string) (string, error) {
	builds := os.Getenv("ERRAND_BUILDS")
	if name == project.MainBuild {
		if at := os.Getenv("ERRAND_BUILD_DIR"); at != "" && filepath.Dir(at) != builds {
			return at, os.MkdirAll(at, 0o755) // ( said by the project )
		}
	}
	return caches.MakeBuild(filepath.Dir(builds), filepath.Base(builds), project.BuildName(name))
}

// BuildDirMain: the same, as a verb
func BuildDirMain(args []string) int {
	name := project.MainBuild
	if len(args) > 0 {
		name = args[0]
	}
	at, err := BuildDirIn(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand build-dir:", err)
		return 1
	}
	fmt.Println(at)
	return 0
}
