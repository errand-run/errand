# Micromamba, Conda

## Micromamba

```python
Micromamba( name = "", python = None, channels = [ "conda-forge" ], packages = [ ], requirements = None,
            pip = [ ], develop = [ ], cuda = None, root_prefix = None, executable = None )
```

A conda environment. What it is depends on what the layer says
( [ADR 0044](../../adr/0044-what-a-layer-builds-is-named-by-what-defines-it) ):

- **declared by its contents** ( `python`, `packages`, `pip`, `requirements`, `develop` ): it is **errand's**. errand resolves
  it where it runs, writes the resolution down in the project's `errand-envs.lock`, builds it in its store on that machine,
  and never changes it once built;
- **only named** ( `Micromamba( name = "x" )` ): it is **yours**, run as `micromamba run -n x …` and never built, updated nor
  judged.

```python
errand.Env( [ errand.Micromamba( "demo", python = "3.12", packages = [ "numpy", "mpi4py" ],
                                 requirements = "requirements.txt", develop = [ "../sdot[viz]" ] ) ] )
```

| | |
|---|---|
| `name` | a label for an environment of errand's ( what `errand envs` shows ); the environment itself for one of yours |
| `python`, `channels`, `packages` | the conda part. `channels` in order: it is their priority |
| `requirements` | a file relative to the project root, its lines added to `pip`. No `-e` line: that is `develop` |
| `pip` | the pip part, resolved by uv over what conda gives |
| `develop` | the packages you work on: installed `-e` over the environment, in an overlay of the project; their dependencies go into the environment |
| `cuda` | `False`: the builds for the CPU, even on a machine with a card |
| `executable` | a micromamba to use instead of errand's own |
| `root_prefix` | for an environment of yours: the directory `-n <name>` resolves against |

## An environment of errand's

**It is named by its resolution, not by its name.** The first command that needs it, on a machine, resolves the declaration
there ( micromamba for the conda part, uv for the pip part ), and the side you typed on writes the result into
`errand-envs.lock`, beside `errand-envs.py`. Commit it: another machine like that one installs exactly those packages,
without resolving. A changed declaration is resolved again, and is another environment; the old one stays until nothing uses
it. `errand setup --update` is not needed to pick up a change: the declaration's hash says when.

**A machine like that one** has the same platform, a card or none as the solver saw it ( `__cuda` ), and meets what the
packages need of the machine ( `__glibc >=2.28`, `__cuda >=12`… ). Two Linux machines with different drivers share a
resolution as long as its packages allow both; otherwise each adds its own to the lock.

**It is kept in errand's store**, on that machine: `~/.cache/errand/main/envs/mamba/<id>/` ( or under the `cache` of an
[`Ssh`](./ssh) ), shared by every project that comes to the same packages, with micromamba's packages beside it, hard-linked.
errand uses its own micromamba, pinned, fetched the first time: nothing to declare on a compute node, and nothing of yours
is read ( no `.condarc`, no `MAMBA_ROOT_PREFIX` ).

**The packages you work on** ( `develop = [ "../sdot" ]` ) are never part of it: their code changes all the time. errand
reads their `pyproject.toml` ( `dependencies`, the extras asked, `build-system.requires` ) and adds what they need to the
declaration; the packages themselves go `-e` into a venv over the environment, per project, in
`errand-outputs/caches/build/`. A dependency you want from conda-forge rather than PyPI goes in `packages`. Dynamic
dependencies are not read: declare them in `pip`.

**Changed by hand, it stops the run.** A `pip install` into it is seen: errand says what changed, and what to write in the
declaration to keep it so. `errand setup --force` builds it again from the lock, beside the one changed; `--no-setup`
runs in it as it is.

**By hand**: `errand shell --env X`, or `errand python --env X`, which prints the interpreter to give an editor ( the
overlay's, when there is one ). `micromamba activate demo` does not know it: it is not in your root prefix.

**Tidied by itself**: an environment that a project's lock holds stays; one that no project declares any more goes after 30
days unused ( 7 on a shared machine ), with the packages nothing links any more.

## An environment of yours

::: info Why the root prefix is pinned
The shell hook exports `MAMBA_ROOT_PREFIX` from an rc file, which only interactive shells read.
Started from a Makefile, from cron or from any plain subprocess, micromamba falls back to the envs
directory beside its own binary, so `-n x` can name a different environment than the same command
typed by hand. The layer pins it: `root_prefix`; else, on this machine, `MAMBA_ROOT_PREFIX`, else
`~/.mamba` or `~/micromamba` when it holds an `envs/`.
:::

Over [`Ssh`](./ssh) only what is declared is used: nothing is guessed from this machine's home.
Behind a [`Slurm`](./slurm) layer the command runs on a compute node that reads no rc file at all, so
state `executable` and `root_prefix` there.

## Conda

```python
Conda( … )      # the same arguments, through `conda` for an environment of yours
```

An environment of errand's is always built with errand's micromamba, whichever of the two the layer says.
