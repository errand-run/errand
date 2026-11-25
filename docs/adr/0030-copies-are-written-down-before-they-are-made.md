---
status: proposed
date: 2026-11-23
---

# A copy is written down before it is made, and removed on demand

The inventory of what errand writes outside projects ( ADR 0029, step 5 ) found one thing nothing removed: `views/<vid>`
in errand's per-user directory, the copy of a page's view on the machine its services run on ( ADR 0027 ). Nobody
knew it was there but the machine that held it, and that machine did not know whose it was.

Asked of Hugo, 2026-11-23: removed on demand, not after a time; removing a folder in `errand web` removes every copy
of what was under it; the agents know of a copy from the moment it is intended, one started later included; and a
result's detail says on which machines it is copied, each removable from there.

## Decision

- **Written down first.** The machine that asks for a copy keeps one record per copy and machine, in its per-user
  directory ( `copies-out/<key>.json`: `path`, `machine`, `kind`, `vid`, `there`, `state` ), written **before** the other
  machine is told anything ( `intended` ), then `there` once made. A directory of files: every agent of the machine
  reads the same record, one started after the copy as well as one that was there; a change rings their bell.
- **The machine that holds says what it holds, and whose**: beside each `views/<vid>`, the origin it was told
  ( host, path ); its agent answers `copies` ( what is held, sizes ) and `copies_remove` ( some of them, by id: only a
  name of a copy, never a path ).
- **Removed on demand**, two ways:
    - the detail of a run says where else it is -- the machine it ran on, when that is not this one; each copy of its
      views -- with a button each ( `remove there` ): that copy goes, what is here stays;
    - removing a run, or a folder of runs ( ADR 0023 ), removes the copies of everything under it, on whatever machine;
      the window says how many, where.
- **A machine that does not answer** keeps its copy for now: the record stays, as `removing`, and the removal is asked
  again when that machine is reached ( its subscription's first table ), at this machine's agent's start, and every
  hour. A record whose result is gone here is treated the same: a copy of nothing is removed.
- Nothing is removed by time. A copy removed is made again, by itself, the next time its page asks for it.

## Considered options

- **After some days unused**: simple, but a large scene copied again every week, and a copy still there for days
  after its result was removed.
- **With its service's end**: a service stops by itself after minutes idle; the copy would be made at each start.
- **The holding machine asking whether the original is still there**: needs the origin up, and reachable from there.

## Consequences

- A copy errand makes of a result is always in a list somebody can read, and in the result's own detail.
- `errand web` reads one more thing for a run's detail ( the records under its directory: a few small files ).
- The TUI does not show the copies yet; removing runs from it removes them all the same ( the same `removal.send` ).

## Done, 2026-11-23

- `errand/copies.py`: `intend`, `made`, `known`, `under`, `remove`, `flush`, `pending` ( the side that asks );
  `into`, `held`, `remove_held` ( the side that holds ). `look.py`: `copies`, `copies_remove`.
- `page_services._keep_copy` writes the record before `copy_into`, which is told the origin; `removal.plan` counts the
  copies under what goes, `removal.send` removes them; `tui/machines` flushes for a machine when it answers again; the
  host agent's hourly round flushes what is pending.
- `web/server.py`: a run's detail carries `copies`; `copy_remove` ( confirmed ) removes one. `web/page.py`: `also on`,
  a line per machine with its button.
- `inventory.py`: `views` and `copies-out` declared with who removes them.
- Tests: `test_copies.py` ( written before made; held and whose; removed on demand, nothing else touched; a machine
  that does not answer, then does; a result gone; runs removed with their copies ).

Seen: a view of 300 kB copied into lmo's cache for a test run of the Mac; the run's page said `also on  Lmo  a copy
of demo.view, for its services  [ remove there ]`; the button, its confirmation, `Lmo: the copy removed`; nothing left
in `~/.cache/errand/views` there, no record here. Not seen: the line of a run that ran on another machine
( `its directory there, where it ran` ) and its button; a removal that waits for a machine and is done when it
answers ( tested with a stand-in, not with a machine switched off ).
