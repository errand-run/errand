# Agents and keeping warm

Without errand, every command pays for its whole path again: an ssh login, a shell reading its rc
file, an allocation, a container entered, a Python importing torch. errand pays once. The first
command leaves small processes behind, **agents**, and the next commands go through them.

| | each time, by hand | through agents |
|---|---|---|
| a run on a machine over ssh | 4–8 s | 0.9–1.8 s |
| a session that imports torch (CUDA), with `Preload` | 2.9 s | 1.5 s |

Nothing to declare: agents are how errand runs anything. They stop on their own when nobody uses
them.

## Where they run

```text
  here                                the machine Ssh leads to               past the layers
  ────                                ────────────────────────               ───────────────
  errand ── this machine's agent ──── one ssh ──── that machine's agent ──── a container's agent, a Slurm node's
             ( carries the command out )            ( owns its jobs, builds )   ( where what one sees changes )
```

- **This machine's agent** carries out what is typed: it reaches the other machines, and keeps the connections between commands.
- **A machine's agent** owns what runs on it: the jobs, their output and their terminal, the builds of `setup`, the services, the machines it
  boots. It is started through one ssh; nothing logs in again.
- **A further agent** only where what one sees changes: inside a container, on the node of an allocation. A venv or a micromamba environment
  is variables and a path, set when a job starts: no agent is needed for it.

Nothing is installed over there: errand sends its program, once per version. → [Nothing to install over there](./layers/ssh#nothing-to-install-over-there)

A job does not depend on whoever started it: the laptop may sleep, an update may come. An agent of a newer errand takes the place over, and
the older one stays until its jobs have ended ( a relay ). Newer is a later release, then a later version, then a later build ( each
build is stamped when it is made ): an older errand still called somewhere ( a copy, another env, a path in a script ) uses the agent in
place rather than take the place back, and nothing needs `errand agents restart`.

What a machine keeps is under its name: its agents, its queue, its ports. On a Mac that is the one of the Sharing settings ( `scutil --get
LocalHostName` ), not `hostname`, which changes with the network.

## Kept warm, for a while

| what | stays up when unused | set by |
|---|---|---|
| an agent | 15 minutes | `ERRAND_KEEP_ALIVE` ( seconds ) |
| ... that keeps a port for a server | as long as its [lease](./serve#a-port-and-until-when) | `errand serve --keep` |
| a Slurm allocation | 1 minute by default; `0`: one job per command | [`Slurm( keep_alive = "30m" )`](./layers/slurm) |
| a Windows virtual machine | 5 minutes | [`Windows( keep = "5m" )`](./layers/windows) |

An allocation kept warm means the next command that fits in it starts at once, without a new
`sbatch` and without entering the layers again. "Unused" means no request and nothing running; an
open connection does not count. So the chain comes down from the far end.

## On a shared machine

A cluster's login node and its compute nodes are shared with others: what errand leaves running there
is what an administrator complains about. errand sees it by itself ( Slurm is there: `sbatch` or
`squeue` on the PATH, or a Slurm job ), or because the environment says so:

```python
errand.envs[ "far" ] = errand.Env( [ errand.Ssh( "login.cluster.org", shared = True ), ... ] )
```

( `shared = False` says a machine is not, whatever it has. ) There:

- an agent goes 2 minutes after its last client, instead of 15;
- a service goes after 10 minutes idle at most, and after 2 hours in all, whatever it declares;
- no port is kept by the system ( `errand web keep` is refused );
- `errand status` says in red what is still up there.

It is the machine's own agent that decides, from what it sees: a flag forgotten on the driving side
still leaves nothing behind on a cluster.

## Warm imports: `Preload`

```python
errand.envs[ "gpu" ] = errand.Env( [ errand.Ssh( host = "gpu-box" ),
                                     errand.Micromamba( "torch" ),
                                     errand.Preload( "torch" ) ] )
```

With [`Preload`](./layers/preload) at the end of a stack, a session is a fork of a process that has
already imported torch. Your project's own modules are always read again. Without the layer, every
session is a fresh Python: your choice.

## Giving way

Something kept warm costs others while it waits: an idle VM holds cores and memory, a process with
torch imported holds memory. Their claim in the [machine's queue](./machine) **gives way**: a command
that needs the room gets it at once, and the VM or the process goes. `errand queue` marks those
claims as kept warm.

A warm Slurm allocation is never stretched. A command runs in it only if it fits: ranks, cores and
cards per task, and `--time` within what the job has left. Otherwise it gets a job of its own. On a
shared cluster, a short `keep_alive` is the polite choice.

## Ctrl-C stops the work

Ctrl-C reaches the work wherever it runs: over ssh, in a container, in an allocation, in a Windows
guest. The work gets an interrupt, then a terminate after 2 s, then a kill after 5 s, with everything
in its process group. The agent over there does this, so it happens even if your connection is
already gone. errand exits with code 130.

## A real terminal

`errand shell` gets a real terminal over there: a pty on Linux and macOS, a pseudo-console on
Windows. Colours, line editing, full-screen programs, Ctrl-C as a key, window resizing.

```bash
errand shell --env cluster      # a shell in the allocation, the container, the environment
errand shell --env win          # cmd.exe in the Windows guest
```

## Listing and stopping agents

```bash
errand agents              # the hub, and each agent it keeps a connection to
errand agents stop gpu     # those whose name contains "gpu"
errand agents stop         # all of them, and the hub
errand agents restart      # all of them stopped, then this machine's agent started again, with its page
```

```text
  hub  pid 79636  up 3 s   ~/.cache/errand/hub-c81a3d1d455a
    lmo                              last used 0 s ago, 1 open; kept 15 min unused
    lmo:env-c81a3d1d455a-32772dfcfc  last used 0 s ago; kept 15 min unused
```

Stopping goes from the far end: an environment before the machine or the allocation it runs in. An
agent that runs something goes all the same, and says what goes on without it: each run has its own
keeper, and the next agent follows it again. `--force` stops the runs too. Two kinds of agents cannot
let go and are left alone while they run something, unless forced: an allocation's ( its Slurm job ends
with it ) and a container's ( it goes with the command that entered the container: a run you wait for
in a container is followed for as long as its command is there; `--batch` is what outlives it ).

It does not depend on the hub: every agent of this machine is found by its directory, whichever errand
started it ( an agent of older sources too, and the one serving the page ), and on each machine reached
lately, the agent there does the same. After a change to errand itself, `errand agents restart` starts
from nothing: everything stopped, then this machine's agent started again from these sources, and the
address of its page said ( the same port and token: a tab already open just reloads ).

While working on errand, a restart is not needed for what runs: an agent belongs to the sources that
started it, and owns no run. A command after an edit goes to agents of the new sources; this machine's
agent of the new sources asks those of the old ones to go, which each does as soon as nobody follows a
run through it -- a command still showing its run keeps its agent until it is done. The runs go on
under their keepers. Then the old agents' directories are removed, and the copies of errand's sources
nothing runs from. Only `--force`, or `errand stop`, ends what runs.

## Watching, without asking again

`errand tui` does not ask each machine what runs there every few seconds: its agent says what changes as it changes -- a run that starts or
ends, the resources when they move, a run recorded in the history.

## What stays on disk

Under `~/.cache/errand` (or `$ERRAND_CACHE_DIR`), errand keeps only what is live: sockets, locks,
pids, the records of running processes, and the copies of itself what runs was started from. Each agent removes, at its start and every
hour, what nothing used for a week. What
piles up belongs to the project, in its `errand-outputs/`:

| what | where | until |
|---|---|---|
| a command's output, kept for `errand attach` | `errand-outputs/state/procs/` | it is brought back |
| run records | `errand-outputs/runs/` | you remove them |
| Windows images | `errand-outputs/builds/windows/` | a newer one is ready |

→ [Where errand writes](./storage)

`ERRAND_WARM=0` starts every session anew, even with a `Preload`. `-v` prints every command errand
starts, including those it hands to an agent. → [When something breaks](./upkeep#when-something-breaks)

The design: [ADR 0005](https://github.com/errand-run/errand/blob/main/docs/adr/0005-the-local-side-orchestrates-agents-execute.md),
[ADR 0008](https://github.com/errand-run/errand/blob/main/docs/adr/0008-nothing-runs-unowned.md),
[ADR 0035](https://github.com/errand-run/errand/blob/main/docs/adr/0035-a-place-has-an-agent-that-owns-its-jobs.md).
