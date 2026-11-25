---
status: proposed
date: 2026-11-23
---

# Declarations are data: written in Python, read as one document

errand is about to have its orchestration written in a compiled language ( Go is the candidate: discussed with Hugo
on 2026-11-23, after ADR 0029 ). The orchestration is the command line, the hub, the agents, the keepers, the sync,
the queue, the page. What a project declares is written in Python today, and executed as Python by all of them: an
environment is an object whose layers have methods ( `wrap`, `build`, `probe` ), a runner is a class.

Two facts make a clean cut possible:

- **Declarations are only read on the machine that asks.** `errand-vars.py`, `errand-project.py`, `errand-envs.py`
  are loaded where the command is typed ( `config.load` ); what goes to another machine is already a request in JSON
  ( `agents/ops.py`: root, env name, tags, what the command line said ).
- **Almost everything declared is already data.** The layers are dataclasses ( `Ssh( host, root, options, … )`,
  `Micromamba( name, python, channels, packages, pip, … )`, `Slurm( partition, cpus, gpus, time, … )`,
  `Apptainer( image, recipe, flags, mounts, … )` ); an `Env` is a stack, a `prepare` stack, tags, `tools`, `needs`;
  the settings are a flat record ( `Settings` ); a runner is "a claim and a command" since ADR 0011.

This ADR is the first of the two contracts to settle before any port ( the other: ADR 0032, what a run says ). It
says what the document is; it changes nothing a user writes.

## Decision

- **The files stay Python**, read on the asking machine only. Python is what errand's users know; their files keep
  working as they are. A Python is needed where a command is typed, nowhere else.
- **One program evaluates them, and prints one JSON document**: `python -m errand.declared ROOT [--time T]`. It does
  what `config.load` does today -- `errand-vars.py` first, the providers ( `errand.providers`, the `errand.envs` entry
  points of installed packages ), then the project's files --, runs today's checks ( `check_stack`, a tag value with a
  comma, an unknown `needs` key, a zone that is not a path of the project ) and says an error as today, with its
  file and line. Whatever orchestrates reads the document; it never imports a project's file.
- **The document is kept** beside the project's state, with what it was made from ( each file's path, size, date
  and hash; the Python; errand's version ): evaluated again only when one of those changes. A command that changes
  nothing starts no Python.
- **The document** ( `"v": 1` ):

  ```json
  { "v": 1, "root": "/Users/me/proj", "errand": "2026.11.20.1",
    "files": [ { "path": "errand-project.py", "size": 412, "mtime": 1795400000.0, "sha256": "…" } ],
    "settings": { "src": [ "pkg", "scripts" ], "exclude": [ "data" ], "build": "build",
                  "outputs_directory": "errand-outputs", "builds_directory": null,
                  "zones": { "/target/criterion": "back" }, "remember_needs": false, "output": "bar", "open": false },
    "default_env": "local", "default_run": null,
    "envs": {
      "lmo": { "stack":   [ { "type": "Ssh", "host": "lmo", "root": "/data/proj", "options": [ ] },
                            { "type": "Micromamba", "name": "g2k-torch", "python": "3.12", "pip": [ "torch" ] },
                            { "type": "Vars", "values": { "G2K_DATASETS": "/data/sets" } } ],
               "prepare": null,
               "tags":    { "driver": "torch", "cuda": true },
               "tools":   { }, "needs": { }, "provided": false } },
    "runners": { "pytest": { "type": "Pytest", "zones": { } } } }
  ```

  A layer is `{ "type": <its class's name>, … its fields }`, a field left at its default left out. A layer inside a
  layer ( `Apptainer( build_on = Ssh( … ) )` ) is a layer, in place. The placeholders a value may hold are kept as
  written, and resolved where the command runs, as today: `{root}`, `{env}`, `{build_dir}`, a tool's `{python}`,
  `{cmake}`, `{errand_include}`.
- **What is not data today**, each named, with what becomes of it:

  | today | where | becomes |
  |---|---|---|
  | `Vars( lambda tags: { … } )` | `layers/overlays.py` | evaluated by `errand.declared` for every combination of the values the environment's tags declare ( `fp = "32\|64"` ): `"values": { "by": [ "fp" ], "of": { "32": { … }, "64": { … } } }` |
  | `Slurm( qos = lambda seconds: … )` | `layers/schedulers.py` | a table is the declarative form: `qos = [ ( "2:00:00", "dev" ), ( "20:00:00", "t3" ), ( None, "t4" ) ]`; a function still works, called by `errand.declared` with the command's `--time`, its answer written with what it was asked ( the document then depends on `--time` ) |
  | a layer class of one's own that changes `wrap` | anywhere | not carried: `errand.declared` refuses it, saying which. In its place, one layer for it, as data: `Wrap( [ "taskset", "-c", "0-7" ], env = { … } )` -- words put before the command, variables added. To be confirmed with the three people who use errand: none of them is known to have written one |
  | a class that only sets a flag on a layer ( `agent_may_detach = False` ) | tests | the flag is a field of the layer, written in the document |
  | a runner class of one's own with a `run` method | `runner/base.py` | a runner is what ADR 0011 says: what it claims ( an extension, a line of the file, a file nearby ), its command with tools named, its build, its list, its jobs form, its zones, how its report is read ( `junit` ). One that needs code of its own is a command it writes itself, which errand runs as any |
  | `errand.value`, `need`, `have` ( this machine's values ) | `errand-vars.py` | unchanged: they are read by the Python that evaluates, and what they gave is in the values of the document |

- **Entries are not declarations.** `test( … )`, `bench( … )`, `Param` are in files of work, found and run by a runner
  where the work runs ( ADR 0011 ): the document says nothing of them.
- **The Python package stays what one imports**: `import errand`, `errand.Env`, `errand.Ssh`, … The classes hold what
  they are given and know how to write themselves; what they do ( wrapping a command, building an image, probing )
  is the orchestration's.

## Considered options

- **Lua, embedded**: no interpreter needed where a command is typed. Hardly anybody who would use errand writes
  Lua; every existing file to rewrite.
- **Starlark, embedded** ( a subset of Python made for configuration, with an interpreter in Go ): the same look,
  nothing to install. No classes, no `import`, no f-strings: today's files need changes, and the providers of
  installed Python packages have nowhere to run. Worth another look if needing a Python where commands are typed
  becomes a burden; the document above would not change.
- **A static file** ( TOML, YAML ): no loops, no values of this machine, no functions of tags: today's projects use
  all three.
- **The orchestration calling back into Python** for each layer's `wrap`: every command pays a Python, on every
  machine; the cut is no cut.

## Consequences

- What a user can write in a declaration is, from now on, what can be written down: a function is allowed where its
  answers can be listed ( `Vars` over tags ), or asked once ( `qos` ).
- The document is something to look at ( `errand envs --json` ), to diff between two machines, to test against.
- A project is read in milliseconds when nothing changed; one more file under `errand-outputs/state/`.
- A layer or a runner that does something new is written in the orchestration, not in a project: the price of the
  cut. `Wrap` and "a runner is a command" are the two doors left open.

## Open

- Whether anybody's project holds a layer or a runner of its own that does more than data: to ask Hugo's three
  users, and to grep their projects, before `Wrap` is settled.
- `Windows`, `WindowsBox`, `Preload`, `Mpi`, `Missing`, `Nix`, `Guix`, `Module`, `Docker`, `Podman`, `Uv`, `Venv`,
  `Conda`: read as dataclasses, each field to be listed in the schema ( not done here: only `Ssh`, `Micromamba`,
  `Slurm`, `Apptainer`, `Vars` were read field by field ).
- The services a project declares ( `errand.services`, ADR 0018, 0026, 0028 ): data too, by what was read of them,
  but not looked at for this ADR.
- The tags' expressions ( `fp = "32|64"`, `--fp 32,64`, `errand/expr.py` ): written in the document as declared, or
  expanded.

## Steps

1. `errand/declared.py`, in today's Python: the document from what `config.load` holds; `errand envs --json`.
2. **Today's orchestration reads it back**: `Env` and the layers built from the document instead of from the files,
   behind a switch, and the whole suite run that way. Whatever fails is something the document does not say: the
   contract is complete when nothing does. No Go needed for this.
3. The table above, one line at a time: `Vars` over tags, `qos` as a table, `Wrap`, runners as data.
4. The schema written field by field ( every layer, every runner ), with the document's version.

## Done, 2026-11-23: steps 1 and 2

- `errand/declared.py`: `document()` writes what `config` holds -- settings, `default_env`, `default_run`, every
  environment ( stack, `prepare` with `{ "same": i }` for a layer both phases share, tags, tools, needs, `provided` ),
  the runners ( by class and what they were given: written, not read back ) --; `install( doc )` builds the
  environments again from it; `errand envs --json`; `python -m errand.declared`.
- A layer is `{ "type", … fields that are not at their default }`; a layer in a layer `{ "layer": … }`; a class of
  one's own that only sets a flag: its base's type and `"_flags"` ( `_`: the document's own -- `flags` is a field of
  `Apptainer` ).
- `Vars( lambda tags: … )`: `{ "by_tags": [ … ], "of": [ [ tags, values ], … ] }`, for every value the environment's
  tags declare and for the declaration itself ( `"32|64"`, what the function is handed when nothing was chosen ).
- `Slurm( qos = [ ( "2:00:00", "dev" ), ( None, "long" ) ] )`: a table, read by `schedulers.qos_for`. The limits are
  written as Slurm writes times.
- **What is not data is listed, not lost**: `not_data: [ { where, why } ]` -- a layer class that changes `wrap`, a
  `qos` that is a function, a value that cannot be written. Today's errand keeps the object; `install` without the
  objects refuses, saying why.
- **Kept**: `errand-outputs/state/declared.json`, with each file's size and hash, the Python and errand's version;
  `declared.fresh( root )` says whether it still holds. A project with something that is not data is not kept.
- **Today's errand reads its own document back** with `ERRAND_DECLARED=1` ( `config.load` ): the whole suite passes
  that way ( 643 passed; the two that failed were a test of this ADR, since corrected, and a test that failed by
  itself too, below ).
- Tests: `tests/test_declared.py`.

Found on the way: the runs' rows kept what a record said under `id( record )`, which a record made after the first
is gone may be given again -- with the same version, since each starts at 0: a test saw the runs of the test before
it, one time in a few ( `test_runs_tree.py`, "the window of a removal" ). A serial per record now.

Not done: the runners read back from the document ( step 3 ); `Wrap`; the schema field by field for every layer
( step 4 ); a document that depends on `--time` for a `qos` that is a function ( it is listed as not data instead ).
