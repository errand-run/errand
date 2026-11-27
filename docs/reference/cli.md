# Command line

```
errand [options] <verb> [arguments]
```

One verb per action. `errand` alone prints the help and runs nothing; `errand <verb> --help` says
what that verb takes. The options that choose environments and paths are everybody's, and may stand
before the verb or after it — but before the target, for `run` and `list`, since what follows the
target is the target's: `errand --env gpu run t.py` and `errand run --env gpu t.py` are the same.

**Running**

| verb | |
|---|---|
| `run [TARGET [ARGS…]]` | run a file or a command (or a suite's entries) in one or several environments, and record it — [Running](/guide/running) |
| `shell` | an interactive shell in an environment. Nothing is recorded |
| `attach [ID]` | follow again what a command left running when its connection was lost, and bring its results back — [Let it run](/guide/detached) |
| `watch` | how the `--batch` work is going, live, until everything has landed |
| `say step\|view\|result …` | inside a run, from any language: a step of its journal, a file of it worth looking at, numbers kept in its result — [What a run says](/guide/protocol) |
| `require [--gpus N] …` | inside a run: more of the machine, asked while it runs; prints what to set — [Sharing a machine](/guide/machine#the-run-asks-from-its-code) |
| `forget ID`, `forget finished` | drop a `--batch` submission from the list, or every one that is over, keeping their results; nothing is stopped ( `errand stop ID` stops it ) |

**What is running**

| verb | |
|---|---|
| `status [--mine]` | what runs on this machine and every machine work was sent to, what waits, what is kept warm, then the `--batch` work — [What is running](/guide/status) |
| `stop [ID...]`, `stop --mine` | stop a run by the id `status` shows, running or waiting in a machine's queue, on any machine; a `--batch` submission by its id; `--mine`: everything this project runs; alone, at a terminal, chosen in a list — [What is running](/guide/status#stopping-a-run) |
| `machines [forget NAME...]` | the machines `errand status` and `errand tui` ask, and forgetting one: not asked any more until work is sent there again — [Status](/guide/status#the-machines-asked) |
| `agents [stop [NAME]]` | the hub and its agents, and stopping them; `--force` even with something running — [Agents](/guide/agents#listing-and-stopping-agents) |
| `vm stop` | stop the [Windows VMs](/guide/layers/windows) kept warm, here and over every `Ssh` |
| `queue` | what this machine has, and what holds it — [Sharing a machine](/guide/machine) |
| `wait` | return once nothing holds the machine and nothing is queued for it |
| `serve [NAME \| -- CMD]` | a server kept while used, under the queue: its port, which starts it when it is used; `serve` alone lists them, `serve stop NAME`, `serve release NAME [--now]` ( its port given back ), `serve connect HASH [PORT]` ( stdin and stdout relayed to the server of that hash; `--hold-end 30s`: the end of stdin is passed on only once the server has answered, then been silent that long ), `serve authorize --to key:NAME [options] -- CMD` ( what an ssh key may ask for here, by its hash ), `serve authorize --to pages --spec -` ( a page's description on stdin: shown, then authorized; `--yes`, `--source URL`, `--path TOOL=PATH` ), `serve authorized`, `serve withdraw HASH [--to WHO]`, `serve fetch`, `serve probe`; `--gpus` `--gpu-ram` `--ram` `--cpus` `-x` `-n` `--name` `--idle` `--on HOST` `--env E` ( where it runs ) `--busy-http PATH[:FIELD]` `--busy-if` `--ready-http` `--ready-if` `--port [NAME=]ANNOUNCED[:SERVER]` ( repeated for several ports, either side empty for errand to choose ) `--others` ( other accounts may connect ) `--listen ADDR` ( other machines may: the port is listened on at that address too ) `--always` ( the system listens on it in errand's stead, and starts errand at a connection ) `--hold-end T` ( of the port: each connection's end held until the server was silent that long ) `--grace` `--keep` ( how long its port stays ) `--var NAME=VALUE` `--cwd` `--version-of FILE` `--server-version` `--wait` ( until it answers ) `--ensure` `--json` `--spec -` ( the request as JSON, on stdin ) `--url [PORT]` ( only its URL, for a script ) `-f` — [errand serve](/guide/serve) |
| `hold [--gpus N] [--gpu-ram S] [--ram S] [--cpus N] [-x] [-n LABEL]` | a claim held while stdin is open: prints `granted <cards>`, reads `warm` / `busy`, prints `evict` — [errand hold](/guide/serve#errand-hold) |
| `web [token]` | every machine's runs, machines and caches, as a page in the browser, served by this machine's agent; `token` prints the token for another browser — [Every run, live in the browser](/guide/web) |
| `tui` | the same, in the terminal — [errand tui](/guide/web#the-same-in-the-terminal-errand-tui) |

**Environments**

| verb | |
|---|---|
| `envs [--json]` | the environments, their tags, their stacks and their state; `--json`: everything the project declares, as one document ( what is not data in it is listed, `not_data` ) |
| `setup [NAME,…]` | build or update environments now, and do nothing else |
| `init` | write a starting `errand-envs.py`, with suggestions in comments |

**Data**

| verb | |
|---|---|
| `download URL [--sha256 H]` | a file into the project's cache, as [`errand.download`](/reference/python-api#download) does; prints its path. The next push carries it |
| `build-dir [--env ENV] [NAME]` | where what the project builds in an environment goes, made and printed — [Build directories](/guide/caching#build-directories) |
| `check [MACHINE...] [--fix] [--bring] [--all] [--json]` | what errand believes of the machines, against what is there: the copies, what the next push would do, the runs in one place only; `--fix` puts right what loses nothing, `--bring` brings back the runs that are there only — [Checking](/guide/sync#checking-errand-check) |
| `python [--env ENV]` | the interpreter an environment runs, on its machine: the overlay's when it has packages one works on, for an editor or `$( errand python ) x.py` |
| `caches [MACHINE...] [--dry-run] [--unused DAYS]` | the library caches a machine holds ( this one by default, else machines of `errand machines` by name ), in the `errand-cache/1` format: what each holds, what is dead, and what no longer serves removed; then errand's store there ( environments, images, installed Windows, [ADR 0044](https://github.com/errand-run/errand/blob/main/docs/adr/0044-what-a-layer-builds-is-named-by-what-defines-it.md) ), what no project declares removed once unused for 30 days ( 7 on a shared machine ); `--unused`: also what has not served for DAYS days ( [ADR 0020](https://github.com/errand-run/errand/blob/main/docs/adr/0020-a-library-cache-says-what-it-holds.md), [ADR 0022](https://github.com/errand-run/errand/blob/main/docs/adr/0022-caches-are-looked-after-per-file-system.md) ). Removing on every machine at once: tab `3` of `errand tui` / `errand web`. `caches hold MACHINE [DIR...]`: MACHINE holds the directories it reaches ( its host agent purges them daily ), e.g. a cluster's login node |
| `require [--gpus N] [--gpu-ram S] [--ram S] [--cpus N] [-x] [--extra]` | inside a run: grows its claim ( to that much in all; `--extra`: that much more than it was started with ), as [`errand.require`](/reference/python-api#require) does, waits, then prints what to set ( `CUDA_VISIBLE_DEVICES=1` ). Nothing outside errand; exit 75 when stopped to run again |

**Suites**

| verb | |
|---|---|
| `list [TARGET]` | what a target designates, and the parameters each entry takes: a dry run — [Running a suite](/guide/runners/) |

## Everybody's options

| flag | |
|---|---|
| `--env NAME` | the environment by name; several under `--grid` ( `--grid=, --env a,b` ), which is why a name cannot hold a comma |
| `-t`, `--env-tags` | an [expression](./expressions) over environment tags |
| `--<tag> VALUE` | one flag per tag name used anywhere in the `errand-*.py` files. Several values under `--grid` |
| `--root PATH` | the project root. Default: found by walking up from the cwd for an `errand-*.py` |
| `-h`, `--help` | the help; after a verb, that verb's |
| `-V`, `--version` | which errand this is |
| `-v`, `--verbose` | every command errand starts, here and over there, on stderr — [When something breaks](/guide/upkeep#when-something-breaks) |

An option that belongs to another verb is an error rather than ignored: `errand shell --batch`
says `--batch` is `run`'s.

## `run` and `list`

The first word after errand's options is the target, read from the command line alone:

- a bare `x`: a `.py` is run by the place's Python, anything else is executed as it is — a file of
  the project, or a command on the place's `PATH` (`make`, `cmake`, `pytest`). A glob runs each
  matching file that can be run, one run each. A directory is an error.
- `path::name` (globs on both sides): the entries of a suite — pytest, Catch2, cargo, errand's own
  entries. → [Running a suite](/guide/runners/)

Several targets are separated by commas — see [Running it](/guide/running#the-target). With no
target, `run` runs `errand.default_run`, the targets the project declares in `errand-project.py`
(from the project root); with none declared it prints this help.

| flag | |
|---|---|
| `TARGET` | `path::name` or a file or command, comma-separated outside braces; `*` `?` `[…]` `{a,b}` on both sides, `**` on the path |
| `-r`, `--runner NAME` | the runner that takes every target, by its name in `errand.runners`, claiming nothing |
| `-e`, `--entry-tags` | an [expression](./expressions) over the tags written on an entry |
| after the target | the target's own arguments: its `sys.argv`, or its command line, passed as written, `--env` and `--` included. A parameter an errand entry declares is read out of them ( `--n=5`, `--n 5` ), and there a comma is a [matrix](/guide/matrices) ( `\,` is a comma, `,,` an empty value ). Anything else is not gridded |

`run` only:

| flag | |
|---|---|
| `-j N`, `--jobs N` | N runs at a time, one process each. A single run whose runner takes it gets N workers of its tool instead ( pytest-xdist, `--test-threads` ). `auto` means as many as the machine has cores. Default 1 |
| `--grid SEP` | several values, split on SEP, make a [matrix](/guide/matrices) — of environments, tags and declared parameters: `--grid=+ --env a+b t.py::x --n=1+2` is four runs. Without it a value is one value, commas included. `run`, `list`, `setup`, `shell` |
| `--batch` | launch and give the shell back — [detached](/guide/detached) |
| `-o` | an output directory of its own for each run — the run's directory under `errand-outputs/runs/` — exported as `$ERRAND_OUT_DIR`, and what it holds brought back |
| `--plain` | the output as the programs wrote it: no gutter before their lines, no colors. `$ERRAND_OUTPUT` says the same for a machine: `bar` ( the default: │ before stdout, ┃ before stderr ), `spaces`, `off`, `plain`; `errand.configure( output = … )` for a project |
| `--out PATTERN` | the same under another tree, or a pattern: `"results/{env}/{when}"`, with `{path}` `{name}` `{when}` `{place}` `{env}` `{params}`; `{params:a,b}` keeps only those parameters, in that order ( a hash of all of them on the end when some are left out ) |
| `--no-setup` | skip the freshness check for this run |
| `--force` | on another machine: what is in the way of the push and is not errand's is moved aside, where the push stops otherwise ( [Smart sync](/guide/sync#the-copy-follows-the-project-carefully) ) |
| `--no-queue` | this run does not wait, and does not hold |
| `--pull DIR` | on another machine: bring DIR ( relative to the project root ) back afterwards. Repeatable |
| `-x`, `--exclusive` | the machine to itself — what a measurement wants |
| `--cpus N` | cores it needs |
| `--ram 8G` | memory it needs |
| `--gpus N` | cards it needs |
| `--gpu-ram 40G`, `--gpu-ram 75%` | what it takes on each of its cards: a size or a share; without it, cards are taken whole — [The memory of a card](/guide/machine#the-memory-of-a-card) |
| `--ranks N` | N MPI processes, each running the target; `--cpus` / `--gpus` / `--ram` are then per rank — [Several processes](/guide/mpi) |
| `--time T` | how long it may take, for a batch system ( slurm's way: `30`, `2:00:00`, `1-12` ) |
| `-m`, `--mem 24G` | memory *ceiling* of the command ( `systemd-run`, where there is one ) |
| `-n`, `--label NAME` | its name in `errand queue` |
| `-l`, `--log FILE` | its output goes there too |
| `-q` | no banners |
| `--at FILE:LINE` | run exactly the entry at that call site ( `FILE:LINE#N`: the entry of rank N on a line that declares several ). This is how errand's own child processes are started; you never type it |

Every run is recorded in `errand-outputs/runs/…`, with or without `-o`; `status`, `attach` and
`--batch` read it there. `-o` and `--out` only give the run a directory to write its own
files in; see [Results and records](/guide/output).

```bash
errand run --env gpu python -c "import torch; print( torch.cuda.is_available() )"
errand run make -j16
errand run -x ./bench --n 1e7
errand run -l build.log -n nightly ./all
```

See [Any command, under the queue](/guide/machine#any-command-under-the-queue).

## `shell`

An interactive shell — yours when the place has it, `sh` otherwise — in the environment named, or
the default one: through its layers, over ssh, inside its container, in an allocation. It takes no
command: `errand run --env X cmd args` runs one.

| flag | |
|---|---|
| `--pull DIR` | on another machine: bring DIR ( relative to the project root ) back afterwards. Repeatable |
| `--no-setup` | do not bring the environment up to date first |

```bash
errand shell --env gpu
```

## `setup` and `init`

| flag | |
|---|---|
| `setup NAME …` | the same as `--grid=, --env NAME,…`; with neither, the default environment |
| `--force` | `setup`: rebuild from scratch. `init`: replace an existing `errand-project.py` |
| `--dry-run` | `setup`: say what it would run, and run nothing |

`init` writes a commented `errand-envs.py`, and an `errand-project.py` with, in comments, the runner
lines that fit what the directory holds and a `default_run` suggestion. Nothing in them is
active until you uncomment it; `run` itself never guesses. It never overwrites without `--force`. See
[Keeping environments current](/guide/upkeep).

## Recipes

```bash
errand run --env cluster nvidia-smi
errand run -o --env gpu train.py --epochs 3
errand run --batch --env cluster --pull out python train.py
errand status --mine
errand stop 6575aafc
errand agents stop cluster
errand machines forget old-box
errand run --grid=, --env gpu,cluster ./bench
errand setup --dry-run
errand queue
errand run -e '!slow' -t 'cuda' 'tests/**::*'
```

## Exit status

Non-zero when anything failed — including a candidate file that could not be imported, which
costs its own row and takes the return code with it ([Running a suite](/guide/runners/)).
A [skip](/guide/configuration#what-only-this-machine-can-supply) is its own status and is not a
failure, but it is never silent either.

## Environment variables

| | |
|---|---|
| `ERRAND_KEEP_ALIVE` | seconds an agent stays with nothing to do. Default 900 |
| `ERRAND_DECLARE_PYTHON` | the Python that reads the project's files — [Installing](/guide/installing) |
| `ERRAND_CACHE_DIR` | where errand's per-user directory is on this machine, instead of `~/.cache/errand` ( its instances are in it ). Over ssh, set it in your shell profile there — [where errand writes](/guide/storage#the-per-user-directory-cache-errand) |
| `ERRAND_INSTANCE` | another errand beside the one in use ( `main` ): everything of errand's has its name, here and on the machines it reaches; `errand instances` lists them — [another errand](/guide/storage#another-errand-beside-the-one-in-use-errand-instance) |
| `ERRAND_OUTPUTS_DIR` | the project's `errand-outputs/` elsewhere, on this machine — [where errand writes](/guide/storage#the-project-errand-outputs) |
| `ERRAND_WARM` | `0`: every session a Python started anew, even with a [`Preload`](/guide/layers/preload) |
