---
status: proposed
date: 2026-11-23
---

# What a run says to errand is a protocol, not a library

A run tells errand things while it runs: where it is ( `errand.step` ), a file worth looking at ( `errand.view` ),
that it needs a card it did not ask for ( `errand.require` ), a number to keep ( `p.results` ). These are Python
functions today, with a few doors for others: shell functions ( `eval "$ERRAND_STEPS_SH"` ), one C++ header
( `errand/require.hpp` ).

Hugo, 2026-11-23: tools such as `errand.view` must be offered in as many languages as possible. And errand "must
stay agnostic: Python is the language we happen to use, not the one errand serves" ( ADR 0011 ). With the
orchestration moving out of Python ( ADR 0031 for the declarations ), Python becomes one of those languages.

Most of it is already a protocol in fact, said as such in one place only ( the steps: "It is the whole protocol",
`guide/steps.md` ). What the code does today:

| what a run does | how, today | open to any language? |
|---|---|---|
| says a step, a curve | one JSON line appended to `$ERRAND_STEPS` ( `<run>/steps.jsonl` ) | yes |
| names a view | a line `{"view": path}` in the same file; the file itself in the run's directory | yes |
| writes its own files | in `$ERRAND_OUT_DIR`, set when one was asked for ( `-o` ) | yes |
| says how it ended | its exit code: 0, 77 a skip, anything else a failure | yes |
| asks for more of the machine | `$ERRAND_PYTHON -m errand require …`, which grows the claim in `$ERRAND_CLAIM_FILE` under the queue's lock, waits, prints `NAME=value` lines to set; exit 75: run me again with that up front | through a Python and errand's sources there |
| downloads a file through errand | `$ERRAND_PYTHON -m errand download URL` ( ADR 0013 ) | the same |
| keeps numbers | `p.results[ … ]`, in the Python that declared the entry | **no**. `ERRAND_RESULTS` was decided for this in ADR 0003, and is not in the code |
| says what it waits for, what to cancel | files in `$ERRAND_PROC_DIR`: `waiting`, `cancel` ( ADR 0009 ) | yes |
| reads its parameters, its tags | `p.n` in Python; `$ERRAND_TAGS`; a plain command gets its arguments | partly |
| finds a cache for a library | `$ERRAND_CACHES/<name>` ( ADR 0019 ) | yes |

So the work is to say it, to close the two gaps ( results; `require` and `download` without a Python ), and to make
a binding in a new language a matter of an afternoon.

This is the second of the two contracts to settle before any port. It changes nothing a run written today does.

## Decision

**The protocol is files, environment variables, exit codes, and one command.** No socket to open, no library to
link: a program that can read a variable and append a line to a file can speak all of it but two calls, and those
two are a command to run.

### What a run is handed

| variable | |
|---|---|
| `ERRAND_PROTOCOL` | **new**: `1`. Absent: not run by errand ( every call below then does nothing, and fails nothing ) |
| `ERRAND` | **new**: the command that is errand, where the run is ( the binary's path ). In place of `ERRAND_PYTHON` and `ERRAND_SRC`, kept as long as a Python errand is what is there |
| `ERRAND_STEPS` | the run's journal, to append to |
| `ERRAND_RESULTS` | **new** ( ADR 0003 ): a file to write the run's numbers to |
| `ERRAND_OUT_DIR` | the run's own directory, when one was asked for ( `-o` ) |
| `ERRAND_TAGS` | the tags of its environment |
| `ERRAND_CACHES` | where a library's cache goes, by its name |
| `ERRAND_BUILD_DIR` | its environment's build directory, when a runner builds |
| `ERRAND_CLAIM`, `ERRAND_CLAIM_FILE` | the machine was granted to this process tree ( nothing in it queues again ); what `require` grows |
| `ERRAND_PROC_DIR` | where its process leaves a word for its keeper: `waiting`, `cancel` |
| `ERRAND_RELAY` | somebody follows this run from another machine: views are said by that side |

### What a run says

- **A step**: one line of JSON appended to `$ERRAND_STEPS`, in one write, ending with a newline:

  ```json
  {"t": 1795400000.12, "text": "iteration 41", "done": 0.41, "residual": 3e-07}
  ```

  `t`: seconds since 1970; `text`: where it is; `done`: from 0 to 1. Any other key whose value is a number is a
  curve. Keys that are neither: `view`, `title`, `last`, `open`, `want`, `transfer`, and every key beginning with
  `_`, kept for errand. Ten lines a second at most is what the readers are made for ( the Python merges the calls
  in between ); a reader ignores a line it cannot read.
- **A view**: a file, or a directory, of the run worth looking at as it evolves. The file is written whole
  ( beside, then renamed: a reader never sees half of it ), then named: `{"t": …, "view": "convergence.html"}`,
  the path from the run's directory; `title`; for a directory, `last`: the files of it to bring after the others
  ( an index that names them ); `open`: what opens it. Named again, it is looked at again.
- **A result**: lines appended to `$ERRAND_RESULTS`, one JSON object each, merged in order into `results:` of the
  run's `result.yaml`: `{"seconds": 1.52, "primes": 9592}`. ( ADR 0003 said flat `key: value` lines; JSON lines are
  what the journal already is, and say a number from a string. To confirm. )
- **How it ended**: its exit code. `0`: PASS. `77`: SKIP. `75`: stopped to be run again with what it required up
  front ( what `require` exits with ). Anything else, or a signal: FAIL, with the end of its stderr.
- **More of the machine**: `"$ERRAND" require --gpus 1 --gpu-ram 75% [--ram 8G] [--cpus 4] [-x] [--extra]`. It
  waits until the claim holds it, then prints one `NAME=value` per line for the run to set in its own process
  ( `CUDA_VISIBLE_DEVICES`, … ); exit 75 passes up as the run's own. The logic -- the queue's lock, the wait, the
  cards chosen -- stays in one place, errand's.
- **A file through errand**: `"$ERRAND" download URL [--sha256 …]`, which prints its path.
- **What it waits for; what must be cancelled if it is stopped**: `$ERRAND_PROC_DIR/waiting` ( JSON: `what`,
  `since`, `job` ), written beside then renamed, removed when it has arrived; `$ERRAND_PROC_DIR/cancel`, one line of
  shell. For what starts jobs elsewhere ( a script around `sbatch` ), rarely for a computation.
- **Its own files**: anything, under `$ERRAND_OUT_DIR`.

### One command for every language

`"$ERRAND" say …` ( **new** ) writes those lines for whoever would rather not: from a shell script, Fortran, a
Makefile.

```bash
"$ERRAND" say step "iteration 41" --done 0.41 residual=3e-7
"$ERRAND" say view convergence.html --title "Convergence"
"$ERRAND" say result seconds=1.52 primes=9592
```

Today's shell functions ( `step`, `view`, `download`, `require` from `$ERRAND_STEPS_SH` ) stay, and become these
calls.

### Bindings

A binding is a convenience over the above, never more: it reads the variables, appends the lines ( or runs
`"$ERRAND" say` ), runs `"$ERRAND" require` and sets what it prints. It does nothing, and raises nothing, without
`ERRAND_PROTOCOL`: a program runs the same by hand.

| language | today | to do |
|---|---|---|
| Python | all of it ( `errand.step`, `view`, `require`, `download`, `out_dir`, `tag`, `p.results` ) | `p.results` through `ERRAND_RESULTS`; the rest unchanged |
| sh | `step`, `view`, `download`, `require` | `result` |
| C++ | `errand/require.hpp` ( header only, C++17 ) | `step`, `view`, `result`, in the same header's manner |
| Julia, R, Rust, Fortran, … | nothing | one file each, when somebody asks; `"$ERRAND" say` meanwhile |

**One test for all of them**: a small program per language that says a step, names a view, writes a result,
requires a card and ends with 77; one test runs each under errand and reads what the run left -- the journal, the
view, `result.yaml`, the claim. A binding is done when it passes.

### What is not in it

- **Entries and their parameters** ( `test( … )`, `bench( … )`, `Param`, `p.n` ): a Python file's way of holding
  several pieces of work, as pytest's or Catch2's is theirs. A runner's business ( ADR 0011 ), in its language.
- **`memo`, `cache_dir`**: Python conveniences over `$ERRAND_CACHES`; a cache is a directory, in any language.
- **The marks a session writes on its output** ( `\x1eerrand-op {…}`: prepared, start, ran, done ): between errand
  and errand. A run never writes them.
- **`ERRAND_PARAMS`, `ERRAND_RANK_*`, `ERRAND_AGENT_*`**: errand's own, to itself.

### Changing it

`ERRAND_PROTOCOL` is a whole number. What is added -- a variable, a key, a word of `say` -- is ignored by who does
not know it, and does not change the number. The number changes when something a run does today stops meaning what
it meant.

## Considered options

- **A library per language, each doing the work** ( the queue's lock, the claim, the rate of the journal ): the logic
  several times, in languages nobody here writes daily; a fix to `require` to be made in each.
- **A socket to the run's keeper** ( ADR 0029 ) for all of it: one more thing every language must speak, where a
  file to append to is enough; and a program run by hand, with no keeper, would need another path. The keeper stays
  dumb.
- **Marks on the standard output** ( as a session's ): mixed with what the program prints, lost when it redirects
  it, and its buffering becomes errand's problem.

## Consequences

- A language errand has never heard of can say steps and views today, and everything once `say` is there.
- `require` and `download` cost a process each time: they are called once or twice in a run, never in a loop.
- What a run is handed is a list somebody can read; a variable not in it is errand's, and may change.
- Python is no longer where a run's side of errand lives: it is the binding most used, and the runner of Python's
  own entries.

## Open

- `ERRAND_RESULTS`: JSON lines, as proposed here, or `key: value` lines, as ADR 0003 said.
- A journal on a file system shared by several nodes ( an MPI run's ranks appending to one file ): one write per
  line holds on a local disk; not looked at on NFS.
- Whether the parameters of a run should be handed to any program ( a variable holding them as JSON ), so that a
  grid ( `--n=1,2` ) reaches a C++ or Julia program without an entry runner of its language.
- Windows: `"$ERRAND"` there, and paths in the journal.

## Steps

1. This table, in the guide: one page, "what a run is handed, what it says", in place of the paragraphs spread
   over `steps.md`, `machine.md`, `output.md`.
2. `ERRAND_PROTOCOL`, `ERRAND`, `ERRAND_RESULTS` handed to every run; `p.results` written through it.
3. `errand say`; the shell functions become it.
4. The test of a binding, with Python and sh passing it.
5. `step`, `view`, `result` in C++, beside `require.hpp`.

## Done, 2026-11-23

- **Handed to every run** ( `cli/here.run_case`, `say.handed` ): `ERRAND_PROTOCOL=1`, `ERRAND_RESULTS`
  ( `<run>/results.jsonl`, merged into `result.yaml` at the run's end, `p.results` over it, then removed ), `ERRAND`:
  a launcher in errand's per-user directory ( `bin/errand-<key>`, one per Python and copy of the sources, declared in
  the inventory, removed with its sources ).
- **`errand say step|view|result`** ( `errand/say.py` ): each line in one write; a curve that is not a number, a word
  that is not something a run says: refused, exit 2; outside a run: nothing written, exit 0.
- **sh**: `result` beside `step` and `view`; `download` and `require` go through `"$ERRAND"`.
- **C++**: `errand/say.hpp` ( `errand::step`, `view`, `result`, `out_dir`, `inside`, `SKIP` ); `require.hpp` calls
  `"$ERRAND" require`.
- **The test of a binding** ( `tests/test_protocol.py` ): the same program in sh with `"$ERRAND" say`, in sh with
  errand's functions, in Python without `import errand`, in C++ with the header ( built with `-Wall -Wextra -Werror` );
  each run must leave the same journal, view, results and status. A program that exits 77 is `SKIP`, its results kept.
- **The guide**: `guide/protocol.md`, "What a run says, in any language".

Not done: `p.results` still goes through the Python that ran the entry, not through the file ( both end in
`result.yaml` ); the run's parameters handed to any program; Julia, R, Rust, Fortran; Windows ( the launcher is
written as a `.cmd`, never run ).

## Amended, 2026-11-26: the scale of a curve

The page draws the curves, and which of them read on a log scale cannot all be guessed. A step's line may
carry `scales`, an object of `"log"` or `"lin"` by curve: `{"t": …, "residual": 3e-7, "scales": {"residual":
"log"}}`. It joins the keys that are no curve. It is an addition, ignored by who does not know it:
`ERRAND_PROTOCOL` stays `1`. Said with the value in every binding: `errand.Log( x )`, `errand.Lin( x )`;
`residual=3e-7:log` to `errand say` and the shell's `step`; `errand::Log( x )` in C++. Not said, the reader
takes log for values all positive spanning more than two decades ( `internal/page/curves.go` ), and
whoever looks may choose otherwise.

A curve's value that is not finite is now left out of its line by Python and `errand say`: JSON has no
`inf` nor `nan`, and a line that held one was not read at all, text and other curves with it.
