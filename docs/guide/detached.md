# Let it run: `--batch` and `attach`

```bash
errand run --batch --env cluster python train.py   # submitted; the shell is yours again
errand status                                      # later: how it is going
errand attach                                      # follow a command whose connection was lost
```

Two ways to stop watching a run without stopping it. `--batch` launches the work and gives the shell
back at once. A command you wait for keeps running when your laptop closes, and `errand attach`
picks it up again.

## Close the laptop

```bash
errand run --env cluster python train.py   # the laptop closes halfway
errand attach                              # later: the rest of the output, the end, the results here
```

What the [agents](./agents) start writes its output to disk where it runs, so a closed laptop or a
dropped network loses nothing. The work goes on, and the command here says so before it gives up:

```text
  the connection to the environment was lost: the work goes on over there ( errand attach )
```

`errand attach` follows it again: it shows the output from the start, waits for the end, and brings
the results back as the command would have. With several left running, it follows each in turn;
`errand attach ID` picks one, by the start of its name ( `2026-09-25_18h04m11-cluster` ). When the
work ended long ago and its output was cleaned up over there, `attach` still brings back what it
wrote in the project.

This is for commands that go through an environment with layers, which is to say through an agent:
another machine, a container, an allocation. A command run here, with no layer, is a child of your
terminal and ends with it; Ctrl-C, unlike a lost connection, stops the work everywhere.
A [Windows virtual machine](./layers/windows) is not followed again yet.

## Launch and come back: `--batch`

```bash
errand run --grid=, --batch --env gpu,cluster python bench.py
```

```text
  submitted b7f3 - gpu on lmo (agent 9d41c0e2), cluster on login.hpc (batch 918273)
  errand status    errand watch    errand attach b7f3
```

`--batch` is a **mode, not a layer**: the run is the same, with the same paths, the same queue and the
same records. It goes **the same way** as a run you wait for -- a session started by the environment's
[agent](./agents), under a keeper of its own -- with one difference: the shell comes back at once. So it
is where every run is: in `errand status`, `errand tui`, `errand web`, whatever project you ask from;
`errand attach b7f3` follows it again, with its output from the start; `errand stop` stops it.

| place | you wait | `--batch` |
|---|---|---|
| an environment ( `Ssh`, `Micromamba`, a venv, … ) | through its agent, output streamed | the same session, let go of at once |
| `Slurm` | an allocation errand holds, with its agent | `sbatch`, which returns as soon as the job is queued |
| no environment at all | a process of this terminal | this errand in a session of its own, which survives the terminal |
| a container | through its agent, inside | the same session, started by the machine's agent, outside: the container is entered for that run, and left when it ends |

Before giving the shell back, `--batch` prepares: the target is resolved and built where the
environment is, and the [plan](./environments#preparing-then-running) comes back here. A mistake in
the command is said now, not tomorrow morning. Then the run is submitted, sized from that plan.

`--batch` does not reach a [Windows virtual machine](./layers/windows) yet: it says so for that
environment.

## Following a submission

```bash
errand status          # what runs everywhere, then each --batch submission
errand watch           # the submissions, live, until everything has landed
errand stop b7f3       # stop it, wherever it was let go ( its record stays )
errand forget b7f3     # drop a submission from the list; its results stay
errand forget finished # drop every one that is over
```

```text
b7f3  run --grid=, --env gpu,cluster python bench.py   2026-09-24 12:04   1/2 [####    ]
    gpu: agent 9d41c0e2 on lmo - finished
    cluster: batch 918273 on login.hpc - running
```

A submission whose results have all landed and come back is **settled**: it is not asked about
again, so `errand status` stays quick however many there are. The settled ones older than a day are
counted on one line.

`errand watch` keeps no state of its own. It reads the tree and asks the batch systems, so it can be
stopped, restarted, or run twice. → [What is running](./status)

## The output tree is the state

Each place writes its plan before anything runs there, so the record of every run has a known
path. **A result file arriving where the plan said it would is the completion signal.** Nothing has
to stay connected for that, and nothing can get out of sync: the [records](./output) every run
leaves in `errand-outputs/runs/` are all there is to follow.

What is kept here, in `errand-outputs/state/`, is only what the tree cannot say: which runs belong to
one submission, and the handle each place gave ( a session's id, a job id ). Only the side that launched
them knows they went together.

A run on another machine, or in a batch job on whichever node the scheduler picked, does not know
its machine's name in advance. Its parameters and its environment are known, so that is what
`errand status` looks for.

## Results come back when you ask

A finished remote run stays over there until something collects it: `errand status`,
`errand watch` and `errand attach` do. → [Results are pulled, not pushed](./sync#results-are-pulled-not-pushed)

## Next

[Several runs: grids](./matrices).
