# Installing

In an environment you have (conda, venv):

```bash
pip install errand-run
```

As a tool, where the system's Python takes no package (Homebrew, recent Debian and Ubuntu):

```bash
uv tool install errand-run
```

or `pipx install errand-run`. The *distribution* is called `errand-run` — `errand` on PyPI is somebody else's project — but what
you type is `errand`:

```bash
errand --version
```

Linux, macOS or Windows. What pip puts in place is the program itself: one file, with no dependency. It carries the program of the
other systems, and nothing needs installing on a machine reached over [`Ssh`](./layers/ssh#nothing-to-install-over-there), in a
container, in a Windows guest: errand sends itself.

A Python, 3.8 or later, reads a project's `errand-*.py`: the one of the environment errand is installed in, else the `python3` on the
path; `ERRAND_DECLARE_PYTHON` names another. It needs nothing installed. A package that declares environments for its users
(`errand.providers`) is found when it is installed in that Python.

## `import errand`, by hand

A run errand starts imports errand from the program. For a file of work run by hand (`python test_x.py`), for an editor, or for a
package that says things to errand when it runs under it, the library alone, pure Python:

```bash
pip install errand-lib
```

## Working on errand itself

```bash
git clone https://github.com/errand-run/errand && cd errand
make install                 # compiled by Go, and installed as a tool ( uv ): the errand that is typed
make lib PY=.venv/bin/python # the Python library in an environment, as it is being edited
make test
make                         # what else there is
```

**Building is an act.** A source that is modified changes nothing to what is typed, nor to what a machine is sent, until
`make install` (or `pip install .`) is run again: one modifies files while computations run. What runs meanwhile goes on as it is. The Python a run imports is
the one the program was built with.

A release is `make release`: a tag, `v2026.11.25.1`, on which GitHub tests, packages and publishes
( [ADR 0037](https://github.com/errand-run/errand/blob/main/docs/adr/0037-distributed-by-pip-built-on-purpose.md) ). Between two,
`errand --version` says how far from the last one it was built: `2026.11.25.1+dev.3.g4f2a1b9.dirty`.

## The examples

[`examples/`](https://github.com/errand-run/errand/tree/main/examples), in the checkout:

```bash
cd examples/01-environments   && errand envs        # four environments over three machines
cd examples/02-existing-suite && errand run '**::*' # pytest + Catch2 + cargo, unmodified
cd examples/03-entries        && errand run '**::*' # errand's own entries, no configuration
```

The first two name micromamba environments, Apptainer images, ssh hosts and a Slurm partition that
do not exist on your machine: read them, adapt them, and `errand envs` tells you none is built yet.
They go with [Four environments](/tutorials/environments),
[Adopting a suite](/guide/runners/adopting) and [Writing entries](/guide/declaring-work).
