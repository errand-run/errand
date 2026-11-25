# What a run says, in any language

```bash
"$ERRAND" say step "iteration 41" --done 0.41 residual=3e-7
"$ERRAND" say view convergence.html --title "Convergence"
"$ERRAND" say result seconds=1.52 primes=9592
```

A run tells errand where it is, what to look at, which numbers to keep, and that it needs more of
the machine. None of it needs a library: it is **files, environment variables, exit codes, and one
command**. A program that can read a variable and append a line to a file can say all of it, in
Fortran as in Python.

Outside errand none of this exists, and nothing fails: a program runs the same by hand.

## What a run is handed

| variable | |
|---|---|
| `ERRAND_PROTOCOL` | `1`. Not set: errand does not run this program |
| `ERRAND` | errand itself, where the run is: a command to call |
| `ERRAND_STEPS` | the run's journal: a file to append lines to |
| `ERRAND_RESULTS` | a file to write the run's numbers to |
| `ERRAND_OUT_DIR` | the run's own directory, when one was asked for ( `-o` ) |
| `ERRAND_TAGS` | the tags of its environment |
| `ERRAND_CACHES` | where a library's cache goes, by its name — [Data and results](./caching) |
| `ERRAND_BUILD_DIR` | where what is built in its environment goes — [Build directories](./caching#build-directories) |
| `ERRAND_BUILDS` | what holds it: another name in it is another build directory |
| `ERRAND_CLAIM_FILE` | its claim on the machine: what `require` grows |

Any other `ERRAND_…` variable is errand's own, and may change.

## What a run says

**A step** is one line of JSON appended to `$ERRAND_STEPS`, in one write:

```json
{"t": 1795400000.12, "text": "iteration 41", "done": 0.41, "residual": 3e-07}
```

`t` is the time in seconds, `text` where it is, `done` how far, from 0 to 1. Any other key whose
value is a number is a curve. → [Where a run is: steps](./steps)

**A view** is a file of the run worth looking at as it evolves, or a directory. Write it whole
( beside, then renamed: nobody sees half of it ), then name it, by its path from the run's directory:

```json
{"t": 1795400001.40, "view": "convergence.html", "title": "Convergence"}
```

Name it again when it changed. → [Every run, live](./web)

**A result** is a line of JSON appended to `$ERRAND_RESULTS`. The lines are merged, in order,
under `results:` in the run's `result.json`:

```json
{"seconds": 1.52, "primes": 9592}
```

**How it ended** is its exit code: `0` passed, `77` skipped, anything else failed, and the end of
what it wrote on its standard error says why.

**More of the machine** is asked with a command, because the queue's logic lives in one place:

```bash
"$ERRAND" require --gpus 1 --gpu-ram 75%      # waits; prints CUDA_VISIBLE_DEVICES=… to set
```

It exits with 75 when the run was stopped to be run again with its needs up front: pass that on
as the run's own exit code. → [Sharing a machine](./machine#the-run-asks-from-its-code)

**Its own files** go anywhere under `$ERRAND_OUT_DIR`. → [Results and records](./output)

## One command for all of it

`errand say` writes those lines for whoever would rather not:

```bash
"$ERRAND" say step [TEXT] [--done X] [NAME=NUMBER ...]
"$ERRAND" say view PATH [--title TITLE] [--last FILE ...]
"$ERRAND" say result NAME=VALUE ...
```

## In your language

| | |
|---|---|
| Python | `from errand import step, view, require`; an entry's `p.results` |
| shell | `eval "$ERRAND_STEPS_SH"`, then `step`, `view`, `result`, `download`, `require` |
| C++ | `#include <errand/say.hpp>` ( `errand::step`, `view`, `result`, `out_dir` ) and `<errand/require.hpp>`; header-only, C++17; `-I{errand_include}` in a runner's build line |
| anything else | the lines above, or `"$ERRAND" say …` |

```cpp
#include <errand/say.hpp>

errand::step( "iteration 41", 0.41, { { "residual", 3e-7 } } );
errand::view( "convergence.html", "Convergence" );
errand::result( "seconds", 1.52 );
```

A binding does nothing more than this: read the variables, append the lines, run
`"$ERRAND" require`. One test holds them all to it: the same small program in each language, and
what its run left.

The design: [ADR 0032](https://github.com/errand-run/errand/blob/main/docs/adr/0032-what-a-run-says-is-a-protocol.md).
