// Several processes of one program ( the guide's "Several processes (MPI)" ): `--ranks N`.
//
// The launcher is the place's, and nothing about it goes in the environment unless it is unusual:
//
//   - an `Mpi` layer names it ( `Mpi( "mpirun", flags = [ "--oversubscribe" ] )` ): its launcher wins, everywhere;
//   - inside a Slurm allocation, `srun`, over the whole allocation, without the variables of the step the agent runs in;
//   - elsewhere, the `mpiexec` of the place -- found where the command itself is found, inside its environment.
//
// The launcher is put next to the program, inside whatever the layers wrap around it: `micromamba run -n x mpiexec -n 4
// python file.py`. Except with a container after a Slurm allocation or an `Mpi` layer: the ranks then start outside
// it, each entering the container on its own node ( `srun … apptainer exec image python …` ).

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/system"
)

func mpiOf(stack []project.Layer) (int, project.Layer) {
	for i, l := range stack {
		if l.Type() == "Mpi" {
			return i, l
		}
	}
	return -1, nil
}

func mpiWords(l project.Layer, n int) []string {
	launcher, np := l.String("launcher"), l.String("np")
	if launcher == "" {
		launcher = "mpiexec"
	}
	if np == "" {
		np = "-n"
	}
	return append([]string{launcher, np, fmt.Sprint(n)}, l.Strings("flags")...)
}

// has: whether the place has a program, asked where the command would be started -- through its layers
func (j *job) has(program string, stack []project.Layer, lctx layer.Context, env []string) bool {
	probe, err := layer.Wrap(stack, layer.Command{Argv: []string{"sh", "-c", "command -v " + layer.Quote(program) + " >/dev/null 2>&1"}}, lctx)
	if err != nil {
		return false
	}
	path, err := kit.LookIn(probe.Argv[0], env, j.cwd)
	if err != nil {
		return false
	}
	cmd := &exec.Cmd{Path: path, Args: probe.Argv, Env: append([]string{}, env...), Dir: j.cwd, SysProcAttr: system.Apart()}
	for k, v := range probe.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd.Run() == nil
}

// srunWords: `srun` starting `n` ranks over the whole allocation
func (j *job) srunWords(n int, plugin string) []string {
	words := []string{"srun", "--overlap", "--mpi=" + plugin}
	if nodes := os.Getenv("SLURM_JOB_NUM_NODES"); nodes != "" {
		var have int
		if fmt.Sscan(nodes, &have); have > 0 {
			words = append(words, "--nodes", fmt.Sprint(min(have, n)))
		}
	}
	for _, pair := range [][2]string{{"SLURM_CPUS_PER_TASK", "--cpus-per-task"}, {"SLURM_GPUS_PER_TASK", "--gpus-per-task"}, {"SLURM_MEM_PER_CPU", "--mem-per-cpu"}} {
		if v := os.Getenv(pair[0]); v != "" {
			words = append(words, pair[1], strings.SplitN(v, "(", 2)[0])
		}
	}
	return append(words, "--ntasks", fmt.Sprint(n))
}

// withoutStep: the surroundings, without what says which step of the allocation this agent is: the ranks are a step
// of their own
func withoutStep(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name := strings.SplitN(kv, "=", 2)[0]
		step := strings.HasPrefix(name, "PMI_") || strings.HasPrefix(name, "PMIX_") ||
			(strings.HasPrefix(name, "SLURM_") && !strings.HasPrefix(name, "SLURM_JOB") && !strings.HasPrefix(name, "SLURM_CLUSTER") && !strings.HasPrefix(name, "SLURM_SUBMIT"))
		if !step {
			out = append(out, kv)
		}
	}
	return out
}

// launcher: the words that start `n` processes in this place, put before the program -- or why there are none
func (j *job) launcher(n int, stack []project.Layer, lctx layer.Context, env *[]string) ([]string, string) {
	if _, mpi := mpiOf(stack); mpi != nil {
		return mpiWords(mpi, n), ""
	}
	if os.Getenv("SLURM_JOB_ID") != "" {
		if _, err := kit.LookIn("srun", *env, j.cwd); err == nil {
			plugin := j.spec.MPI
			if plugin == "" { // the one the MPI of the environment speaks: PMIx for Open MPI, PMI-2 for MPICH
				plugin = "pmi2"
				if j.has("ompi_info", stack, lctx, *env) {
					plugin = "pmix"
				}
			}
			*env = withoutStep(*env)
			return j.srunWords(n, plugin), ""
		}
	}
	if j.has("mpiexec", stack, lctx, *env) {
		return []string{"mpiexec", "-n", fmt.Sprint(n)}, ""
	}
	return nil, fmt.Sprintf("%d ranks were asked for, and this place has no launcher: `micromamba install -c conda-forge mpich mpi4py` in its environment, or an Mpi layer that names one", n)
}

// command: what a job starts -- its target as the layers of its place make it, with what starts its ranks when it
// has several. -> the words, the variables the layers add, where the target's own first word is among them
func (j *job) command(stack []project.Layer, lctx layer.Context, env *[]string) (layer.Command, int, string, error) {
	target := j.argv
	n := j.spec.Ranks
	box := containerAt(stack)
	if box >= 0 { // the ranks start outside the container, each entering it: this agent is not the container's
		inner, err := layer.Wrap(stack[box+1:], layer.Command{Argv: target}, lctx)
		if err != nil {
			return inner, 0, "", err
		}
		entered, err := layer.Enter(stack[box], lctx, nil, inner.Argv)
		if err != nil {
			return entered, 0, "", err
		}
		var before []project.Layer
		for _, l := range stack[:box] {
			if l.Type() != "Mpi" {
				before = append(before, l)
			}
		}
		prefix, why := j.launcher(n, stack[:box], lctx, env)
		if why != "" {
			return entered, 0, why, nil
		}
		out, err := layer.Wrap(before, layer.Command{Argv: append(prefix, entered.Argv...), Env: inner.Env}, lctx)
		return out, -1, "", err
	}
	wrapped, err := layer.Wrap(stack, layer.Command{Argv: target}, lctx)
	if err != nil {
		return wrapped, 0, "", err
	}
	at := len(wrapped.Argv) - len(target)
	if n <= 1 {
		if at < 0 {
			at = -1
		}
		return wrapped, at, "", nil
	}
	prefix, why := j.launcher(n, stack, lctx, env)
	if why != "" {
		return wrapped, 0, why, nil
	}
	same := at >= 0
	for i := 1; same && i < len(target); i++ {
		same = wrapped.Argv[at+i] == target[i]
	}
	if !same { // a layer made one word of the command ( a shell line ): the launcher goes in with it
		out, err := layer.Wrap(stack, layer.Command{Argv: append(prefix, target...)}, lctx)
		return out, -1, "", err
	}
	argv := append(append(append([]string{}, wrapped.Argv[:at]...), prefix...), wrapped.Argv[at:]...)
	return layer.Command{Argv: argv, Env: wrapped.Env}, at + len(prefix), "", nil
}
