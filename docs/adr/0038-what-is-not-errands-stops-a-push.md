---
status: proposed
date: 2026-11-25
---

# What is not errand's stops a push; a place may have its cache; a journal is watched

Asked by Hugo on 2026-11-25, after an otrec run on lmo. Three decisions, each amending an earlier one.

## A push never removes nor replaces a file errand did not write ( amends 0006, 0019 )

ADR 0006 moved such a file to `.errand-copy-kept/`, said so, and went on: after 30 days it was gone. And it printed
every deletion, the file errand had written itself included, which nobody needs to read. Now:

- **What errand wrote there** and the project has no more goes without a word ( `-v` lists it ).
- **What errand did not write** -- never, or not as it is now -- stops the push before anything is done. The files are named.
  The user removes them, declares a zone, or says `--force`: moved aside as before, and kept 30 days.
- "Changed since errand wrote it" counts as not errand's: its contents are somebody's. Hugo, the same day: it is an error,
  and is to be said. Not so once it was brought back ( `--pull`, a `back` zone ): the project holds it, and the copy's
  manifest notes it ( `have`, at the end of a pull ).
- **What a directory holds without a manifest is never errand's**, even where it is the same as here ( Hugo ): it is
  left as it is, date included, and replacing or removing it stops the push. Only what errand wrote, errand owns.
- Asked of the disk, by the agent, of each path the push would touch ( `lose`: a `stat` each ): the trusted manifest may
  not know of what appeared since the last walk.
- **The manifest says whose each file is** ( `foreign` ): it held what a walk found as if errand had written it. What a copy that holds this one, or that it holds, wrote is errand's.
- What a push writes is said before it is in place ( `.errand-copy.writes`, a line each, gone when the push ends ): one that
  is cut, its agent killed even, does not make its own files somebody's. A line whose file is not as it says counts for nothing.
- A manifest of before this rule ( no `foreign` ) does not say whose its files are: it is as no manifest ( Hugo, the same day ).
- `errand setup --force` rebuilds, and is no such word: only `errand run --force` is.

## A place may say where errand keeps its own ( amends 0022's "a setting of the machine" )

`Ssh( cache = "$SCRATCH/errand" )`. On a cluster the home may be a slow NFS, shared with machines that do not see the
fast disk: `ERRAND_CACHE_DIR` in a shared profile, or a link, would be wrong for those. The path is part of what a place is ( its key, its
note ): two of them on a machine are two places, with one queue. `~` and `$NAME` are expanded over there; an unset one is an error.

Not `Slurm( cache = … )`: an allocation's agent and the login node's speak through that directory, which both must see.

## A run's journal is watched by the system ( amends 0024 )

ADR 0024 had each run nudge its agent, and set aside file notifications: not in Python's standard library, and different on
each system. ADR 0032 then made a step a line appended to a file, in any language, with no socket to open: nothing nudges any
more, and the Go agent was looking every second, and every 250 ms for a follower. The system tells ( inotify, kqueue, both in Go's standard library ): `system.Watch`
on a run's directory and its journal, by the agent that holds the run. Windows looks twice a second. A run a batch system
runs on another node writes to a disk where nothing tells: every 2 s, for those alone.

The page was told `*` at each change, and read its lists and its detail again each time: it is told which run moved, and reads that
one; what a run wrote is read from where the last reading ended ( `run_look`, one question ). The waiting page of `--open` reloaded every
2 s until a run elsewhere had come back, looking for it by its place here: it is found by its note, and told by the events.

## Where a view opens is errand's to say ( amends 0010, 0032 )

`view` printed `OUTPUT: file://…` in the run's own output, with the path of where it ran: no path of here, for a run
elsewhere. Nothing is printed in a run any more ( `ERRAND_RELAY`, which silenced it, is gone ): errand says it at the run's end, on the side
one looks from, on a terminal or not. Outside errand, the binding shows it itself, a line on the error output ( Python, C++ ), and
`errand.view` is always True: a library that has called it has nothing to print.

## Views of a run that goes elsewhere ( 0017 )

A view named again without `open` lost how it opens: the page showed the directory instead. Merged now. A directory view of a
run on another machine was read here only once the run was over. It is brought to its place here while a page shows
the run, each time it is named again, its `last` files read first and put in place last. ( ADR 0017 had whoever follows the run bring it, page or not. )

## `errand check`: what errand believes, against what is there

Asked by Hugo, the same day, about a copy he would rather remove by hand: errand is not told. It bears it -- the next push walks, a run not
brought back is said `gone` --, but its records may be wrong, and nothing said so. `errand check [MACHINE…]` looks, and the page has it per machine
( the same command, `--json` ). What a push would do is a push that only looks ( `sync` with `dry`: the far side refuses to write ).

`--fix` is for what loses nothing, each thing looked at again by the side that does it, and only what errand itself wrote down;
`--bring` is asked apart, as it may bring a thousand runs removed here on purpose. A foreign file, a run: never removed by it.

A copy's lock says who holds it ( `owner.json` ): one whose process is gone, on this machine, is nobody's at once. On a disk shared
by several machines, a lock of another machine's is still waited for, half an hour at most.
