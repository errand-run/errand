---
status: proposed
date: 2026-11-23
---

# errand in Go is a new program, built from what errand must do -- not a translation

An earlier plan ( ADR 0033, not kept here ) ported errand piece by piece, each piece standing in for its Python counterpart behind the Python's own
contracts, checked by comparing the two on the same state. That gave a keeper, an agent ( processes, table, history,
subscription, the copies it holds, the caches, the storage report ), the sync: 9,000 lines of Go that the Python's suite
runs through as they are.

It also had a cost that grew with each piece: the Go wrote what the Python writes, down to its accidents -- a
parameter set named by the bytes of Python's `json.dumps`, a file read as `decode( "utf-8", "replace" )` reads it, a
report whose keys are those a screen in Python happens to expect. Faithful, and not what one would write.

Hugo, 2026-11-23: "avec cette écriture en Go, on n'est pas loin de repartir de 0 ( en tous cas, on ne cherche pas la
compatibilité ), et tout est permis pour réaliser le cahier des charges avec un code propre et robuste."

## Decision

- **What is specified is what errand does for who uses it**: the guide, the reference, the ADRs' decisions ( nothing
  runs unowned, a job has its keeper, files cross by manifest, a machine is shared through its queue, a run says
  things by a protocol, declarations are data ). Not the Python's files, messages or function boundaries.
- **A new program, `errand`, in Go** ( `go/errand`, the same module ), grown from the bottom up on the pieces already
  written, each kept where it is right and rewritten where it only copied:

- **The Python errand stays as it is, whole, until the Go one does what it does.** No more half-and-half: the
  switches that put a Go piece under the Python ( `ERRAND_AGENT_BIN`, `ERRAND_KEEPER_BIN` ) stay while they are free,
  and are not a constraint on the Go any more. A project is moved from one to the other when the other is ready for
  it; the two never share a run.
- **What changes freely**, where it makes the program simpler or safer -- each change said in the ADR of its piece:
    - **files**: JSON, everywhere ( `result.json`, `launch.json`, a claim, a record ); one schema version in each;
      written beside then renamed, always;
    - **messages**: one framing, one JSON per message, requests that name their version; an error is a field with a
      code, not a sentence to search;
    - **names and layout** of what errand keeps under its per-user directory and under `errand-outputs/`;
    - **behaviours that were accidents**: a row's id is a hash of what it is, not of how Python prints it; text that is
      not UTF-8 is replaced Go's way; a report says what it measured, not what a screen wanted.
- **What does not change without Hugo**: what a user types ( the verbs and options of `reference/cli.md` ), what a
  project declares ( `errand-*.py`, ADR 0031 ), what a run says ( ADR 0032 ), and the rules that protect work and
  files ( ADR 0006's "what a sync would lose is kept", 0008, 0019, 0029 ).
- **Tests are the program's own**: Go tests for each package, and end-to-end tests that drive the `errand` binary
  as a user does -- each written from the guide and from the Python test of the same behaviour, which is where the
  cases are. 
- **Python is left with two things**, `python -m errand.declared` ( the declarations, read where a
  command is typed ), and the binding for a run written in Python ( `from errand import test, step, …` ) with what runs
  such a file's entries.

## Consequences

- No user of the Python errand is disturbed while the Go one is written; nothing is "half ported" in between.
- The Go agent's answers that copied a Python accident are cleaned when their caller is written in Go, not before:
  until then the page and the screen that read them are the Python's.
- The order is the table's, top to bottom: each layer is useful as soon as it is there ( `errand queue` and
  `errand wait` with the queue; `errand run` here with a run and a project; `--env` on another machine with the bridge ).
- More to write at once than a translation: the command line and the layers are a design, not a copy.

