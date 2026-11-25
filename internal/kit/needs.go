package kit

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/errand-run/errand/internal/queue"
)

// NeedsFrom: NEEDS among `args` -> the needs, whether the machine is asked whole, what is left of the arguments
func NeedsFrom(args []string) (queue.Needs, bool, []string, error) {
	var n queue.Needs
	exclusive := false
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, glued := strings.Cut(a, "=")
		take := func() (string, error) {
			if glued {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s: a value is missing", a)
			}
			i++
			return args[i], nil
		}
		var err error
		var v string
		switch name {
		case "-x", "--exclusive":
			exclusive = true
		case "--gpus":
			if v, err = take(); err == nil {
				n.GPUs, err = strconv.Atoi(v)
			}
		case "--cpus":
			if v, err = take(); err == nil {
				n.CPUs, err = strconv.ParseFloat(v, 64)
			}
		case "--ram":
			if v, err = take(); err == nil {
				n.RAM, err = queue.ParseSize(v)
			}
		case "--gpu-ram":
			if v, err = take(); err == nil {
				n.GPURam, err = queue.ParseShare(v)
			}
		default:
			rest = append(rest, a)
		}
		if err != nil {
			return n, false, nil, fmt.Errorf("%s: %v", name, err)
		}
	}
	return n, exclusive, rest, nil
}
