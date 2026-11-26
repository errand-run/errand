# Every run, live in the browser

```bash
errand run -O --env lmo python train.py     # -O, --open: the run's page opens in the browser
errand web                                  # every run of every machine, as a page
```

Each run has a page, and it is live: the console as the program writes it, its step and progress
bar, its curves, its images and HTML pages redrawn as the run writes them, wherever it runs. No
`scp` to look at a figure, no `tail -f` over ssh: the page follows the run, and stays once it ends.

`errand web` is the same for everything: what runs on every machine you sent work to, what waits,
every run done as a tree with a table of its parameters and results, the machines' gauges, the
library caches they hold. Pick a run, and its page is on the right.

Neither one runs anything itself. What runs is owned by the agents over there: close the tab, nothing
stops. Nothing is asked again and again either: the agents say what changes, when it changes, and the
page shows it at once.

## Where else a result is

A run's detail says `also on` when the result is on another machine too: the machine it ran on, when
that is not this one, and each copy errand made of one of its views for the view's services
( a scene shown by a Blender running on a GPU box ). Each line has `remove there`: that copy goes,
what is here stays. Removing a run, or a folder of runs, removes all of them: nothing is left
anywhere of a result that is gone. A machine switched off keeps its copy until it answers again;
errand asks then, by itself.

errand writes a copy down before making it, and never removes one by time: one removed is made again
the next time its page needs it.

## A run's page

`errand run` prints, under each run's head, the address of its page; `-O` ( `--open` ) opens it
in the browser by itself, a tab per run for the first few, then the main page:

```text
  ▸ train  lin  lmo
    ↗ http://127.0.0.1:20501/r/3f2a9c1e
```

What it shows, as it happens:

- **the console**, stdout and stderr, as the program writes them;
- **where the run is**: the text and bar of its last [step](./steps), the estimated end, how long
  since the last step ( a run that hangs shows ), a curve per value it reports;
- **its views**: the files it names with [`errand.view`](./steps#view-a-file-that-evolves), an image,
  an SVG, an HTML page, brought back from the machine that runs it as soon as they change, and shown
  in place. A directory that carries its own page ( `errand.Static` ) is served as it is, while the
  run is still writing it;
- **its files**: `output.txt`, `result.json`, what it wrote in its directory, as a tree. A folder is folded until it is opened
  ( a click, `→` ), and listed again as the run writes. The path above what is shown leads back, as `←` and backspace do;
- **JSON** as a tree that folds: what is short on its line, what repeats as a table, `.jsonl` by its lines; `raw` shows it as written.

```python
for it in range( n_iter ):
    ...
    errand.step( f"iteration {it}", done = it / n_iter, loss = loss )
    if it % 50 == 0:
        plot_mesh( mesh, "mesh.png" )
        errand.view( "mesh.png", title = "current mesh" )    # on the page a moment later
```

The address holds no secret, and works for a run on any machine: the page is served by this
machine's agent, which asks the others.

## Runs

Tab `1`. What runs and what waits on top, with the [servers](./serve) kept on every machine, then every run done, as a tree:

```text
otrec/
  experiments/exp_2d_real.py
    foam
        n      nb_splits reduction | loss   status  when     runs
        10000  1         1         | 0.005  PASS    12:13    34
        2000   1         8         | 0.002  PASS    12:14    1
          ▸ its runs: date, machine, env, status, duration
sdot-ffi/
  …
```

- **The tree** is the path of the file from the project's root, the file, the entry, then its
  **parameter sets**: the runs whose parameters are equal, whatever machine or environment.
- **The table** has a column per parameter, then the results the run declared, the status and date
  of the last run, how many runs. `s` sorts by the next column.
- **Removing**: `d` removes the runs under the row, `D` keeps only the latest of each set and
  environment, after a confirmation. `⇧↑ ⇧↓` chooses several rows.
- **Runs elsewhere**: the list is this machine's record ( every run started from here has its
  directory here ). `c` checks the other machines for runs not here ( `◇` ), `b` brings one back.

## The detail

Right of the list, the row chosen: its run's page ( above ), its facts, its views and outputs. The
output chosen is shown below them, text, image or HTML, and opens whole in a tab of its own.
A row can also be killed, through every layer, once confirmed.

## Machines

Tab `2`. Each machine of the ledger: its gauges ( CPUs, memory, cards ), measured only while shown,
what holds it, its VMs and guests, what is kept warm, its servers: those that are held, with their address, their clients,
their command and their journal, the ports kept for them ( `k` gives one back ), and, in grey, those that ran there in the last week and stopped, with why. Then what pages
and ssh keys may ask for there ( `authorized`, its whole definition, to whom, since when, last used; `withdraw` takes it back;
[authorizations](/guide/serve#those-who-may-not-say-a-command-authorizations) ). What a page asks for is authorized in a window of
its own, which the page opens ( [a page that asks for a service](/guide/serve#a-refusal-and-the-window-that-authorizes) ). `a` switches a machine's gauges off or on ( what
runs there is followed all the same ), or watches a dormant one; `r` gives it a
name, `f` forgets it until work is sent there again.

## Storage

Tab `3`. Where errand keeps things, one row per **mount**, with the machines that see it:

```text
▾ /home/h  nfs c3po:/users/anedp/h          shared by cinaps, node14 · 1 directory of errand's
    errand's own    /home/h/.cache/errand · the same directory for cinaps, node14
▾ cinaps  /workdir2                          xfs on cinaps · nfs 10.15.25.1:/workdir2 on node14
  ▾ loom                                     1 place · 58 entries · 35.7 MB
▾ Lmo  /data                                 ext4 /dev/sdb1 · this machine's own
    Projects-rsync  /home/leclerc/Projects-rsync · 625.7 MB · synced 2 min ago
```

Machines share some directories and not others: a cluster's home is one for all its nodes, each
node's `/scratch` is its own. So errand decides per directory: each directory it makes or uses holds a
small id file, and two machines that read the same id share it. The mount only groups them:

- **shared by** A, B: a directory on it reads the same id from both;
- **probably shared by**: the same network source seen from both, no shared directory seen yet;
- **this machine's own**: a local disk; a disk its server exports to others is one row, named after
  the server's disk, with how the others see it ( `nfs … on node14` ).

Unfolded, a mount shows errand's directories on it ( its own, the copies of projects, the service envs,
the code fetched for services ), then its [library caches](./caching#a-library-s-caches-kept-tidy):
per cache, a row per place, then its entries merged across places, with their size and when they
last served.

- `i` removes the dead entries, `u` those unused for n days ( `n` sets n, 30 by default ), of one
  place or, on a cache's row, of every place;
- `d` removes the entry under the cursor where it is dead or unused, `D` everywhere it is;
- `h` has the place's machine hold the directory: its agent purges it daily;
- `v` shows every entry, or only the dead and unused.

## Keeping its address

The page is served by this machine's host agent, which stops when unused: a bookmark then finds nothing.

```bash
errand web keep           # the system keeps the page's port and the stores'
errand web keep off       # gives them back
```

With `keep`, `launchd` ( macOS ) or `systemd --user` ( Linux ) listens on the page's port and the stores' in errand's
stead. At the first connection it starts a small relay, which starts the host agent if need be and passes the
connection on; after five minutes without one the relay goes, and nothing of errand's runs until the next. The host
agent listens behind it on ports of its own, so it can stop, restart, or hand over to a newer errand while the page
keeps its address ( and its login ). `errand status` lists what the system keeps. Not on Windows for now, never on a
shared machine.

## From another machine

`errand web` opens the page in the browser and prints its address. It is served by this machine's
host agent, on `127.0.0.1` only, one per user and machine. The port is open to every user of the
machine, so the page is served only to who holds a token: `errand web` opens it with a code used once,
then a cookie keeps it. `errand web token` prints the token, for another browser.

From another machine, a tunnel:

```bash
ssh -L 20501:localhost:20501 lmo     # then errand web there gives the URL
```

## The same in the terminal: errand tui

```bash
errand tui
```

The same tabs, the same tree and detail, in curses, for a session with no browser. `w` opens the
run under the cursor in the browser.

| key | |
|---|---|
| `1` `2` `3` | runs, machines, storage |
| `↑ ↓` `pgup pgdn` | move · `→ ←` unfold, fold · `tab` the list or the detail |
| `enter` | open an output as this machine opens it · `e` in `$EDITOR` · `o` in `$PAGER` · `w` the run's page |
| `/` | filter: machine, project, command, state · `p` this project or all |
| `k` | kill, once confirmed · `x` a shell on its machine, where it works |
| `R` | ask every machine again now |
| `?` | every key · `q` leave, nothing stops |

The page takes the same keys, with buttons beside them.

## Next

[What is running](./status) · [Where a run is: steps](./steps) · [Data and results, computed once](./caching)
