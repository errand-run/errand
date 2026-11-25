package page

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

// checkedBy: `errand check` of one machine, as the page shows it -- the command itself, so that both say the same
func checkedBy(m *UiMachine, fix, bring bool) (map[string]any, error) {
	name := m.Name()
	if m.Here() {
		name = "here"
	}
	args := []string{"check", "--json"}
	if fix {
		args = append(args, "--fix")
	}
	if bring {
		args = append(args, "--bring")
	}
	cmd := exec.Command(kit.SelfPath(), append(args, name)...)
	cmd.Env = reach.WithTyped(os.Environ())
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	data, err := cmd.Output() // ( it ends with 1 when it found something )
	var got struct {
		Machines []struct {
			Error string `json:"error"`
			Found []struct {
				Kind, Level, Text, Fix, Done string
				Lines                        []string
				Count                        int
			} `json:"found"`
		} `json:"machines"`
	}
	if jerr := json.Unmarshal(data, &got); jerr != nil || len(got.Machines) != 1 {
		var ended *exec.ExitError
		if errors.As(err, &ended) && len(ended.Stderr) > 0 {
			return nil, errors.New(kit.FirstLine(strings.TrimSpace(string(ended.Stderr))))
		}
		return nil, fmt.Errorf("errand check said nothing of %s", m.Name())
	}
	said := got.Machines[0]
	if said.Error != "" {
		return nil, errors.New(m.Name() + ": " + said.Error)
	}
	items, fixable, there, done := []any{}, 0, 0, []string{}
	for _, c := range said.Found {
		switch {
		case c.Done != "":
			done = append(done, c.Done)
		case c.Fix == "":
		case c.Kind == "there":
			there += c.Count
		default:
			fixable++
		}
		items = append(items, map[string]any{"level": c.Level, "text": c.Text, "lines": c.Lines, "fix": c.Fix, "done": c.Done, "kind": c.Kind})
	}
	return map[string]any{"machine": m.Name(), "found": items, "fixable": fixable, "there": there, "done": done}, nil
}
