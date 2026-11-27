# Venv, Uv

## Venv

```python
Venv( python = "python" )
```

An interpreter that is there: a venv's python, or any installed one. errand never builds it, nor installs anything
into it ( [ADR 0044](../../adr/0044-what-a-layer-builds-is-named-by-what-defines-it) ): a venv of errand's is a `Uv` one.

```python
errand.Env( [ errand.Venv( python = ".venv-313/bin/python" ) ] )
```

## Uv

```python
Uv( name = "", python = None, requirements = None, pip = [ ], develop = [ ] )
```

A venv of errand's, made by uv in errand's store on the machine where it runs, and never changed once built.

- **Without packages of its own** ( no `requirements`, no `pip` ), it is the project's environment, as its `uv.lock`
  says: `uv sync --frozen` into the store, and the project itself installed `-e` over it, in an overlay. The `uv.lock`
  and `pyproject.toml` are its identity: changed, they make another venv.
- **With them**, they are resolved once for every platform ( `uv pip compile --universal`, with the hashes of every
  file ), written down in the project's `errand-envs.lock`, and installed exactly so on every machine.

| | |
|---|---|
| `name` | a label |
| `python` | the version uv installs it with ( the exact one is written in the lock ) |
| `requirements`, `pip` | its packages, locked with the hashes of their files: no `-e` nor local path ( that is `develop` ), no option of pip |
| `develop` | the packages you work on, `-e` in an overlay of the project; their dependencies go into the venv |

Everything else is as for [an environment of errand's](./micromamba#an-environment-of-errand-s): the store, a change by
hand that stops the run, `errand python --env X` for an editor, the tidying. There is no `.venv` in the project.

uv does not need to be installed: where it is neither on the `PATH` nor in `~/.local/bin` or `~/.cargo/bin`, errand
fetches one from astral.sh into its cache over there ( `~/.cache/errand/uv`, never on the `PATH`, never in a shell
profile ). A cluster's nodes are such a place.
