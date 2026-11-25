// The bridge to a place's agent ( ADR 0035 ): what one `ssh` starts on the machine a command reaches.
//
//	errand there        its standard streams joined to the socket of this machine's agent; never typed by hand
//
// The agent is started first when it is not there, and outlives the bridge: it is not at the end of the ssh pipe
// ( ADR 0005 ). A connection that drops takes the bridge with it, and nothing else.

package reach

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/errand-run/errand/internal/kit"
)

// updateWord: what a bridge says, in place of a link, when the errand that reached it is behind this machine's
const updateWord = "errand-update: "

func ThereMain(args []string) int {
	if newer := NewerHere(); newer != "" {
		fmt.Fprintf(os.Stderr, "%sa newer errand ( release %s ) is in use on this machine, and the one that asks is release %s: update it\n", updateWord, newer, Release)
		return 3
	}
	conn, err := ReachAgent()
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand:", err)
		return 1
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin) // the near side went, or the way to it: only this bridge goes with it
		conn.Close()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	return 0
}

// DecodeInto: a request's field, read as the type that says it
func DecodeInto(v any, into any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// MakeCopyRoot: the directory a copy goes in -- where it leads, when it is a link to a directory that was removed
func MakeCopyRoot(root string) error {
	err := os.MkdirAll(root, 0o777)
	for at, links := root, 0; err != nil && links < 8; links++ {
		target, lerr := os.Readlink(at)
		if lerr != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(at), target)
		}
		if at = target; os.MkdirAll(at, 0o777) == nil {
			return nil
		}
	}
	return err
}

// PrepareCopy: where a project's copy goes, made; and, when errand's outputs for it were asked elsewhere ( a scratch
// disk ), that place made and linked in the copy under their usual name. What was written before the link is moved.
func PrepareCopy(root, outputs string) (string, error) {
	root = CopyRoot(root)
	if err := MakeCopyRoot(root); err != nil {
		return "", err
	}
	if outputs == "" {
		return root, nil
	}
	outputs = CopyRoot(outputs)
	if err := os.MkdirAll(outputs, 0o777); err != nil {
		return "", err
	}
	here := filepath.Join(root, "errand-outputs")
	if st, err := os.Lstat(here); err == nil && st.Mode()&os.ModeSymlink == 0 && st.IsDir() {
		entries, _ := os.ReadDir(here)
		for _, e := range entries {
			if !kit.Lexists(filepath.Join(outputs, e.Name())) {
				_ = os.Rename(filepath.Join(here, e.Name()), filepath.Join(outputs, e.Name()))
			}
		}
		if err := os.Remove(here); err != nil {
			return root, nil // not empty: left as it is, and used as it is
		}
	}
	if target, _ := os.Readlink(here); target != outputs {
		_ = os.Remove(here)
		if err := os.Symlink(outputs, here); err != nil {
			return "", err
		}
	}
	return root, nil
}

// CopyRoot: as ssh's `cd` reads it -- from the home folder
func CopyRoot(path string) string {
	home, _ := os.UserHomeDir()
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	return path
}
