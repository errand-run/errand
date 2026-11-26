---
status: accepted
date: 2026-11-26
amends: 0018
---

# A request that waits keeps its place for what it waits for, not for the whole machine

ADR 0018 made the queue first come, first served for every request: whatever arrives after a request that waits, waits behind it,
even when it would fit now ( "no backfill for now" ). The reason stands: a steady stream of small runs must not starve a benchmark, nor a
service waiting for a card.

Seen on lmo, 2026-11-26: a test that needs no card stayed 10 minutes behind a run that had been waiting 1 h 36 for the card. Everything
queued behind that run, whatever it needed. The run that waits for a card does not lose anything when a run that takes none goes first:
the order bought nothing there, and cost everybody else.

## Decision

**A request that waits keeps for itself what it asked, and a later one passes it only with what is left.** Checked under the queue's
lock, against the requests ahead of it ( older ones, and every claim that grows ):

- **Cpus and memory are counted.** What those ahead asked is counted as taken, on top of what is held. A later request that still fits
  goes; one that would take from what they asked waits behind them ( `fit, which is waiting for cpus too` ). A request that asks for
  none of a resource never waits for it, as a claim that asks for none is counted against none.
- **Cards are not counted, they are kept.** A later request that needs a card ( or memory on one ) waits behind any request ahead that
  needs one too ( `a card too` ), even if a card it alone could share is free. Counting reservations per card, with shares and sizes,
  would be backfill proper: not needed by what was seen.
- **The machine to itself keeps everything.** A request ahead that is exclusive, or that asks for more of something than the machine
  has ( it runs alone ), is passed by nothing. A later one that is exclusive, or runs alone, waits behind whatever is ahead
  ( `its turn` ).
- **A claim that grows** keeps what it asks on top of what it holds; more cards, or more memory on them, is a card.

`errand queue` and the run's record name the first request it waits behind, and why, as before.

What is kept warm ( `Try` ) follows the same rule: it is taken if it takes nothing a request ahead waits for.

## Considered options

- **Backfill with time estimates** ( Slurm's ): a later request passes if it ends before the earlier one would start. errand does not
  know how long a run lasts. Rejected.
- **A queue per resource**: a run that needs cpus and a card would hold a place in two queues, and could wait in each for the other.
- **Counting cards as cpus are counted**: a card given to a later request may be the one the earlier one would get; with shares and
  sizes per card, which cards a set of waiting requests would take is a placement, not a sum. Left until it is needed.

## Consequences

- A test, a build, a run on cpus only goes beside a run that waits for a card, as long as it leaves that run the cpus and memory it
  asked for.
- A run that asks for nothing ( a slot ) is never held back by one that waits, except by one that wants the machine to itself.
- A run that waits keeps exactly what it would have had under ADR 0018: nothing that passes it takes what it asked.

## Steps

1. *Done 2026-11-26*: `queue.behind` ( `internal/queue/queue.go` ), tests `TestWaitingForACardHoldsBackOnlyCards` and
   `TestTheWholeMachineKeepsItsPlace`; the guide's "First come, first served" says it.
