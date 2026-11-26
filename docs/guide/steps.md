# Where a run is: steps

```python
import errand

for i in range( n ):
    r = solver.iterate()
    errand.step( f"iteration {i}", done = i / n, residual = r )
```

```text
$ errand run --env cluster python solve.py
…the output scrolls here…
iteration 41 · ▕████▏      ▏ 41 % · 2 min left · residual 3e-07 ↘
```

A run that takes hours says nothing until it ends: an iteration count, a residual going down, a
picture getting better are lost among the lines, or never printed. **`errand.step`** lets the run
say where it is, and errand shows it live: at the bottom of `errand run`, in `errand status`, in
`errand tui`. Every call is also written down, dated, beside the run's [record](./output).

## step

```python
errand.step( text = None, *, done = None, **values )
```

One call, three kinds of content, all optional:

| | | shown as |
|---|---|---|
| `text` | the current state, as a person reads it. It stays until another one is given | the text |
| `done` | a fraction from 0 to 1 | a bar, and an estimated end |
| `name = number` | named numbers, one **curve** each | the last value and its trend; a sparkline in the TUI, a [plot](#the-curves) on the page |

```python
errand.step( "assembling" )                       # just the text
errand.step( residual = r, energy = e )           # two points, the text unchanged
errand.step( f"epoch {k}", done = k / epochs, loss = loss.item() )
```

Values are numbers: something `float()` cannot convert raises, as a typo would.

**Call it as often as you like.** errand writes at most ten times per second; the calls in between
are merged ( the last text, the last `done`, the last value of each curve ) and written at the next
write, at the end of the case, or when the process exits. The last state is never lost; the curves
are sampled at 10 Hz at most.

**Outside errand, it does nothing.** A script run by hand, or a process errand did not start, calls
`step` for free: nothing is written, nothing raised ( a [view](#where-it-opens) is said where it is ).

### The scale of a curve

A residual or a loss reads on a log scale, most other numbers on a linear one. errand guesses: **log
for values all positive that span more than two decades**, linear otherwise. Where the guess is not
the right one, the run says it with the value:

```python
errand.step( loss = errand.Log( loss ), cfl = errand.Lin( cfl ) )
```

`errand.Log( x )` and `errand.Lin( x )` are `x`, with the scale of its curve. Saying it once is enough;
the last one said holds.

## view: a file that evolves

An HTML page, an image redrawn as the run goes: that is not a step, it is a file of the run worth
watching. Name it once:

```python
errand.view( "convergence.html" )                   # the process writes it itself
errand.view( "convergence.html", content = html )   # errand writes it
errand.view( "mesh.png", title = "current mesh" )
```

A relative path is in the run's directory, beside `output.txt`, so it comes back with the record,
from wherever the run is. Call `view` again with new `content` each time the file changes.

### Where it opens

A program that calls `view` prints nothing of it: where to look depends on who looks, which errand knows.

- **While the run goes**, its [page](./web#a-run-s-page) shows it, wherever the run is.
- **When it ends**, under the run's last line -- of `errand run`, or of `errand attach` for a `--batch` --, each view is
  said where it is **on the machine one looks from**, a link a terminal opens with a click:

  ```text
  ╰ PASS solve  12 s
      ↗ convergence.html  file:///Users/me/proj/errand-outputs/runs/solve/2026-11-18_10h02m11-lin@lmo/convergence.html
  ```

- **Outside errand**, for a script run by hand, `view` says it itself, once per view, on the error output:
  the same line, with the path the script wrote. A directory with its own page is said as the `errand open` that opens it.

So a library calls `errand.view( path )` and no more; only where `errand` cannot be imported does it
have something to say itself.

::: warning Replace it, never rewrite it in place
A reader must never see half a file. With `content`, errand writes a temporary file and renames it
over the old one. A process that writes the file itself must do the same:

```python
tmp = out / ".convergence.html.tmp"
tmp.write_text( html )
tmp.replace( out / "convergence.html" )
```
:::

### A directory, and what opens it

A view may be a directory: frames written one by one, with an index that names them. Each time the
run names it again, what changed in it comes back, not the whole of it. `last` names the files that
must come after the rest ( an index never names a frame not here yet ):

```python
errand.view( "foam.sviz", last = "manifest.json", open = errand.Static( "index.html" ) )
```

`open` says what opens it, so nothing is declared elsewhere. `errand.Static( "index.html" )`: the
directory carries its own page, and it is served as it is, from the copy brought back here, beside
[`errand web`](./web), while the run is still writing it. The run's page shows it among its views.

## In a shell script

```bash
eval "$ERRAND_STEPS_SH"

for i in $( seq 1 100 ); do
    ./iterate
    step "iteration $i" done=$( awk "BEGIN { print $i / 100 }" ) residual=$( cat residual.txt )
done
view convergence.html
```

`$ERRAND_STEPS_SH` holds the functions `step` and `view` ( which prints nothing, as above ), in POSIX sh, and `download URL`, which prints
the path of a file fetched as [`errand.download`](/reference/python-api#download) does, and `require`,
which grows the run's claim as [`errand.require`](/guide/machine#the-run-asks-from-its-code) does. The first argument of
`step` is the text unless it reads `name=value`; the values are written as given, so they must be
numbers. `residual=3e-7:log` ( or `:lin` ) says the [scale](#the-scale-of-a-curve) of its curve. There is
no merging: each call is a line.

## Where it shows

**`errand run`**: on a terminal, one line at the bottom, rewritten in place while the output scrolls
above it, and erased when the case ends. Off a terminal ( a pipe, a CI log ) nothing is drawn: the
journal is in the record. A run on another machine sends its line back with the rest, and it is
drawn here; it never gets into `output.txt`.

**`errand status`**: the same line, after `running`:

```text
    lmo   3f2a9c1e  run solve.py  --env lin  12 min  running · iteration 41 · ▕████▏      ▏ 41 % · 2 min left · residual 3e-07 ↘
```

**`errand tui`**: the process row shows the text and a bar. Its detail shows:

- the text, the bar and the estimated end;
- how long ago the last step was: a run that hangs shows;
- one sparkline per curve, on its [scale](#the-scale-of-a-curve);
- the views, first among the run's files. Enter opens one; an `.html` or `.svg` view opens in the
  browser.

A finished run of the history shows the same, read in its directory.

### The curves

On the [page](./web#a-run-s-page), a run that reports numbers that move has an entry **steps** after
`console`. It draws the curves chosen, one small plot each, each on its own scale, as the run goes:

- the line of a value, among the facts at the top, is a switch: a click puts its curve on the plots, or
  takes it off, and shows them. The same switches are above the plots;
- at first, the first six curves that move are shown; one that stays constant is there to be chosen;
- the button at the right of a plot's name switches it between `log` and `lin`. On a log scale, what is
  zero or negative is not drawn, and the plot says how many;
- `x: time` is the time since the journal's first line, `x: point` the points one after the other ( the
  journal's: ten a second at most, so not always the iterations );
- the pointer reads every plot at the same place.

What is chosen, curves and scales, is kept by the browser for the script: its next run opens the same way.

## The journal

Every call is a dated line of **`steps.jsonl`**, in the run's directory:

```json
{"t": 1763220000.12, "text": "iteration 41", "done": 0.41, "residual": 3e-07}
{"t": 1763220000.95, "residual": 2.1e-07}
{"t": 1763220001.40, "view": "convergence.html"}
```

```text
2026-11-15_14h02m11-lin@lmo/
  result.json
  output.txt
  steps.jsonl      every step, dated
  convergence.html a view
```

It is the whole protocol for steps ( for everything a run says, in any language: [What a run says](./protocol) ). A process finds the file through **`$ERRAND_STEPS`**, set for each case,
so a program in any language can say its steps by appending JSON lines to it, one per write. The
keys `t`, `text`, `done`, `view` and `title` have the meaning above; every other key with a number
is a curve. `scales` says on which scale some are drawn: `{"scales": {"residual": "log"}}`.

It is plain JSON lines: load it with `pandas.read_json( path, lines = True )` to plot the whole
history of a run. Watching a long journal costs nothing: the screens read only its first and last
64 KB.

## step and track

`step` is **inside one run**: where it is now, and its curves as it goes. [`track`](./declaring-work#test-bench-track-experiment)
is **between runs**: a number followed from one run to the next. A solver can do both: steps while
it iterates, and its final residual as a tracked result.

## Next

[Results and records](./output) · [What is running](./status) · [The screen](./web)
