---
status: accepted
date: 2026-11-25
---

# Distributed by pip, one wheel per system; built on purpose

## Decision

- **pip, because everybody knows it** ( Hugo ): `pip install errand-run` in an environment one has, `uv tool install errand-run` or `pipx` as a
  tool. The line is the same on every system; pip chooses the wheel.
- **One wheel per system, and what is typed is the program itself** ( Hugo: no wrapper ). A single wheel for every system needs a script that
  chooses at each command: 22 ms for a short command, against 6.
- **Each wheel carries the programs of the other systems** ( `share/errand`; 22 MB in all ): a machine reached by ssh is sent the one it runs,
  with no compiler and no internet there, or here.
- **The Python that reads a project is the one of the environment errand is installed in**, found beside the program; else the `python3` on
  the path; `ERRAND_DECLARE_PYTHON` names another. A package that declares environments is found where errand is installed.
- **`errand-lib` is another distribution** ( Hugo ): `import errand`, pure Python, for a file run by hand, an editor, a package that says things to
  errand. Neither needs the other. A run that errand starts imports the copy the program carries, of its own version: a library edited in
  place changes nothing to it.
- **Building is an act** ( Hugo: "on peut vouloir modifier les fichiers et lancer en parallèle des calculs. La compilation devrait être un acte
  volontaire" ). Nothing is compiled because a source changed, neither what is typed nor what a machine is sent. `pip install .` is the act:
  everything is compiled before anything is replaced, and what runs goes on, from its own copy ( ADR 0035 ).

## Versions and releases ( Hugo, 2026-11-25 )

- **A version is the day and a counter**, `2026.11.25.1`: the first that is neither tagged nor on PyPI. The two distributions have the same.
- **The version is the tag's, and is written nowhere in the sources**: `lib/version.py` asks git. What is not a release says so,
  `2026.11.25.1+dev.3.g4f2a1b9.dirty` -- three commits after, and modified --, and PyPI takes no version with a `+`.
- **One branch, `main`**, tested at each push, on Linux, macOS and Windows.
- **A release is asked for from a person's machine** ( `make release` ), as a tag on a commit of main that GitHub has. GitHub tests that commit on every
  system, makes the packages once, installs and uses them on each, and publishes those files if all of it passed. What did not pass is
  published by nobody; its number is spent, and the next one follows.
- **A candidate is a number, not a branch**: `2026.11.25.1rc1` ( `make prerelease` ), which pip takes when it is told `--pre`.
- **No key is kept**, here or on GitHub: PyPI knows the workflow ( trusted publishing ).

## Considered options

- *Binaries to download, Homebrew, `errand update`*: lines that differ from one system to another, a signing key to keep. The bare programs can
  be put in a release for whoever wants no Python.
- *One wheel and a launcher*: tried; slower at each command, one more thing between what was built and what runs.
- *An editable install that recompiles*: what is typed would change while one edits.

- *Published from a person's machine*: nothing to set up, but what is published was tested on one system, and a key is kept there.
- *Branches for development, candidates and releases*: merges between them, after which what was tested is not what is published. A branch
  and a pull request, for a piece of work that wants them, are as they always are.

## Costs

- Five wheels of 22 MB per version, most of it the same.
- Go is needed to install from the sources, never to use a wheel.
- A release waits for GitHub, and for its slowest system. On Windows, what drives the program through a shell is not tested.
