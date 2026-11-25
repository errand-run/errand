---
status: proposed
date: 2026-11-24
---

# Entries are listed, a run carries several, and any tool's code asks for what it needs

Runners are the last large piece the new program ( ADR 0034, ADR 0035 ) does not have. Before writing them, Hugo and
the session that carries the port went over what they are, on 2026-11-24. This is the record of that discussion:
what was decided, in Hugo's words where they decide, and what is still open. Nothing of it is written yet.

## What is there

- ADR 0011: a run is one invocation of a tool, and a runner is "a claim and a command"; errand no longer lists the
  cases, and makes no run, record, claim or directory per case. ADR 0031: a runner is data. The Python is not there
  yet: a runner is a class whose methods run **inside the environment, in errand's own process** -- it reads files,
  installs pytest with pip, asks `cargo metadata`, reads a report. In the new program nothing of errand runs inside a
  venv or a conda environment ( variables at launch, ADR 0035 ), and a place's agent is Go: a runner's code has
  nowhere to run.
- ADR 0014: `errand.require( gpus = 1 )`, `"$ERRAND" require`, `errand/require.hpp` can be written anywhere in a
  run's code, in any language. The claim grows **in place**, and the call waits. Stopping to be run again exists for
  one case only, two runs that grow and wait for each other ( exit 75 ). Inside a batch system's allocation `require`
  does nothing, and ranks are not asked this way at all: they are an argument of errand's own entries
  ( `test( "…", ranks = 4 )` ) or of the command line.
- What Hugo's projects use ( grep of 2026-11-24 ): loom, otrec and sdot-ffi hold 952 entries of errand's own in 127
  files, 457 `Param`, two `ranks =`, no tag on an entry, and not one line about `errand.runners`: no runner
  configured, none written.

## Decision

### An entry and a run are two things

Hugo: "c'est dommage de se priver de lancer les entrées des tests en parallèle lorsque c'est possible. […] J'avoue
qu'une interface homogène avec gestion des ressources pour couvrir les différents outils me plairait bien."

The first design held three things together, and ADR 0011 threw them out together: knowing the entries, one process
per entry, and what is recorded per entry. Only the second was heavy.

- **An entry** is a piece of work its tool can name: a pytest test, a Catch2 test case, a test of a cargo target; for
  a script, the file itself. It is what is **listed**, **selected**, **recorded**, **run again**, and what **asks**
  for a resource.
- **A run** is one invocation of the tool ( ADR 0011 stands ), carrying **a set of entries**: one process tree, one
  claim in the queue, one directory.
- **errand composes the sets.** By default the entries of a file go in one invocation, as today. `-j N` spreads the
  entries of a command over N invocations at a time. An entry that asks for what its run does not hold is run again
  apart, with those that ask for the same.

So what runs in parallel is errand's doing, the same for every tool, and every invocation goes through the queue:
`pytest-xdist` is no longer needed, and Catch2, which has no parallelism of its own, gains one. `Runner.jobs`
( ADR 0011: `-j N` on a single run handed to its tool ) goes.

### A runner is data, read by the place's agent

What a runner says, all of it data ( ADR 0031 ):

| | |
|---|---|
| what it takes | name patterns, a line the file must hold, a file nearby ( `Cargo.toml` ); its level, for ties |
| its build | a command, once per command and per environment, in the preparation ( ADR 0011, unchanged ) |
| how it lists | a command, and the pattern that reads a name from a line of what it prints |
| how it runs | a command with the tools named ( `{python}`, `{cargo}` ), and how a set of names is handed: as words, joined, or in a file |
| its report | one of the forms the agent reads ( `junit`, Catch2's XML, cargo's text, the exit code ): a status, a duration and numbers **per entry** |
| what its tool needs | packages the environment's own machinery installs ( records, locks, requests that wait without internet: ADR 0035 ), not a `pip` the runner calls |
| its zones | as today ( ADR 0006, ADR 0011 ) |

| tool | lists with | several entries in one invocation | checked |
|---|---|---|---|
| pytest | `--collect-only -q`: one node id per line | the node ids as words | yes, 2026-11-24 |
| cargo | `cargo test -- --list`: `name: test` | `-- --exact a b` | yes, 2026-11-24 |
| Catch2 | `--list-tests`, once built | names joined by commas, or `--input-file` | no |
| GoogleTest | `--gtest_list_tests`, once built | `--gtest_filter=a:b` | no |
| a script, a command | nothing: it is its own one entry | -- | -- |

A runner that cannot list says nothing of it, and a file is then one entry. A project's own runner is such a record
( `errand.Runner( takes = …, list = …, run = …, report = "junit" )` ); one that needs code of its own is a command
the project writes, which speaks the protocol of ADR 0032 as any run does.

### Where each thing happens

Hugo: "Je suppose qu'il vaut mieux le faire au bout de l'env."

- Which files a target names, and which runner takes each: the machine's agent, on the copy -- globs and what the
  runners say they take need no environment. A typo is said before anything is built or allocated.
- The build: in the preparation's place ( ADR 0011 ).
- The listing: **at the end of the environment**, after the build, by the tool itself -- only there is the tool,
  what the file imports, the binary. ADR 0002 stands.
- The plan -- the entries, the sets, their runs -- is held by the agent that owns the runs ( ADR 0035 ), which is
  what `status`, `watch` and whoever draws read.

### What a run needs is asked from its code, by any tool

Hugo: "il faudrait réfléchir à la question de demande de ressources en se disant que ça doit fonctionner avec
n'importe quel outil ( pas que les entrées d'errand ). […] des commandes à mettre où on veut dans le code qui font
échouer les entrées en demandant une ressource si elle n'est pas présente. Dans ce cas, il faut rejouer l'entrée une
fois les ressources obtenues."

**One request, two answers.** `require` ( ADR 0014, ADR 0032 ) asks the agent in charge of the resource.

- **Granted in place** when it can be given without starting the run again -- cards, memory, cores of a machine, or
  of an allocation that has them: the claim grows, the call waits, then returns ( ADR 0014's reason: no work lost ).
  Hugo: "Ok pour garder l'attente sur place quand la machine peut accorder sans relancer."
- **Stop, and be run again with it** otherwise: ranks, what an allocation does not have, two runs that wait for each
  other. What was one rare case in ADR 0014 becomes the general answer.

**The entry that asks names itself, and stops in its tool's words.** No entry has to be known before for this: the
request says what is asked and who asks.

| tool | how the entry is known | how it stops |
|---|---|---|
| pytest | `PYTEST_CURRENT_TEST`, which pytest sets and `"$ERRAND" require` inherits ( checked ) | pytest's skip |
| Catch2 | the current test's name, read in `require.hpp` ( not checked ) | `SKIP` ( not checked ) |
| cargo | the test thread's name ( checked: `tests::alpha` ) | to find: a test of libtest cannot skip while it runs |
| a script | the run itself | exit 75 ( ADR 0032 ) |

The other entries of the invocation go on. When it ends, errand runs the entries that asked again -- those of a file
that asked for the same thing in one invocation -- with what they asked claimed up front. An entry that asks again
for what it was just given fails, and that is said.

**The request travels with the run's end**, by the way its status already comes back ( for a node of a batch
system, the run's file on the disk it shares ): no link has to stay open for it. It goes up the agents that started
the run, to the first that can grant it: the allocation's agent ( `srun`, when it fits in the allocation ), the
login node's agent ( another allocation ), the machine's agent ( its queue ). Those agents are there by construction:
an agent stays while it has jobs or dependents ( ADR 0035 ), `--batch` and a closed laptop included. Only waiting in
place needs a way to the agent while the run goes on, and it is the one downloads take ( requests that wait as
files ). Hugo: "demander une ressource au milieu de l'entrée signifie que les agents en charge des ressources
doivent rester en place, avec toute la chaîne de communication… c'est un choix fort mais qui m'a l'air raisonnable."

**Ranks are a resource asked this way**: `require( ranks = 4 )`. An entry written for MPI says so in its code, in any
language; `--ranks` on the command line stays. The `ranks =` argument of errand's own entries goes.

**Nothing is remembered from one run to the next.** `remember_needs` ( ADR 0014, optional, off by default ) is not in
the new program. Hugo: "Je préfère qu'on ne fasse pas remember_needs du tout. C'est un peu un comportement caché."

### An entry does not choose its environment

Hugo: "la sélection de l'env est faite par l'user dans sa ligne de commande. Les entrées peuvent juste sauter si l'env
qu'elles attendent n'est pas le bon ( à voir si on fait un coverage ), mais elles ne pourront pas sélectionner un env."

`has_tag`, `tag` and the tool's own skips stay what they are. Since each run's report gives a status per entry, a
table of entries by environments -- passed, skipped, never run -- can be drawn from the records; whether errand
draws it is left open.

### errand's own entries

Hugo: "les entrées errand ont été faites parce qu'elles sont bien pratiques... mais ça brouille le message d'errand.
Je ne sais pas s'il faut le garder et le mettre en avant dans le projet mais je serais frustré de devoir m'en passer.
En attendant, je propose de se focaliser sur les outils usuels que tout le monde utilise."

They stay, as one tool among the others: a command of the Python binding ( `{python} -m errand.entries …` ) that
lists, runs a set and reports as the table above says. They are written after pytest, Catch2, cargo and scripts,
and the documentation does not open on them ( already ADR 0003's positioning ).

## What this revises

- **ADR 0011**: "errand no longer lists the cases" -- it lists them, where the tool can. Its reason stands: no
  process, no claim and no directory per case. `Runner.jobs` goes; `ranks =` on an entry goes.
- **ADR 0014**: `require` inside an allocation asks instead of doing nothing; stopping to be run again is the
  general answer where the claim cannot grow in place; `remember_needs` goes.
- **ADR 0032**: a request says who asks ( the entry ), and exit 75 is one way among others of stopping.
- **ADR 0031**: the fields of a runner as data are those of the table above.

## Considered options

- **One process per entry**, as before ADR 0011: clean, and what made runners heavy. Measured on Hugo's Mac, warm:
  `import jax` 0.27 s, `import torch` 0.63 s -- times 526 entries for sdot-ffi, before any fixture a session shares.
- **No listing; entries only name themselves when they ask.** Enough to run an entry again, not to spread a file's
  entries over several invocations, which is what Hugo asked for.
- **The tool's own parallelism** ( `pytest-xdist`, `--test-threads` ): one more thing to install per tool, none for
  Catch2, and work the queue does not see.
- **Everything by stopping and running again**, no wait in place: one path, and work done again each time a card is
  only busy.
- **Needs remembered** for the next run: a behaviour nobody wrote down.

## Costs

- One listing per command, at the end of the environment: for Python, every file imported once more, in one
  process. Not measured cold, on a cluster's network disk.
- pytest's session fixtures are computed in each invocation, and tests that depend on their order break when they
  are spread: as with `pytest-xdist` today.
- Names handed back to a tool need that tool's escaping, and a file when there are many.
- Under a batch system the listing is one more job; the allocation kept and shared ( ADR 0035 ) makes it cheap, but
  the allocation is asked before the entries are known.

## Open

- A grid of arguments without declared parameters ( the open point of ADR 0032 ): under `--grid=SEP`, an argument
  after the target that holds the separator would be an axis, for any program. Proposed, not discussed.
- How a cargo test stops when it asks, and what its report says of it.
- Whether a listing is kept from one command to the next ( by the file's content ), and what makes it stale.
- An invocation that ends without its report ( a crash ): the entries without a verdict are known, and could be run
  again one by one to find which one it was.
- How an allocation is sized before the entries are listed.
- The coverage table, and what `errand list` shows.
- Whether a file's entries that share nothing should be spread without `-j`, when the machine has room.

