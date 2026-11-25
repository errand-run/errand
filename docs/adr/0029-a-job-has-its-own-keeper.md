---
status: proposed
date: 2026-11-23
---

# A job has its own keeper; agents own no job

Agents were made to coexist, one per version of errand's sources ( ADR 0005 ), so that an edit to errand does not
interrupt what runs: a command after the edit goes to agents of the new sources, the old ones finish their runs,
then go. The price showed on 2026-11-23:

- **Two paths.** A `--batch` run over ssh is started with `nohup`, outside any agent ( ADR 0008, "by design" ): it
  is in no machine's table. `errand web`, `errand tui` and the head of `errand status` did not show two runs of
  `guitar_2000` that ran for 15 minutes on lmo; only that project's own list of submissions knew them.
- **Old agents stay**, with what they hold: 13 `host-…` directories on lmo, 5 on the Mac ( 4 of them dead, each held
  back by the record of a service's keeper nobody came back for ). "A new version removes what the old one left"
  cannot be said simply while an old agent may hold a run.
- **A long run depends on an old agent**: the newer errand must still know how to read it, stop it, attach to it.

The cause is one coupling: a process an agent starts writes into **that agent's pipes** ( `procs.Proc._pump` ). The
agent gone, nobody can follow the process any more, so it is an orphan to kill ( `kill_orphans` ). Everything else an
agent keeps of a process is already files: `meta.json`, `out`, `err`, `exit`.

Discussed with Hugo on 2026-11-23. Two ideas of his -- every job under `nohup` with its facts in files; or two
agents per machine, a dumb one that is never updated and holds the processes, a clever one that is -- meet in one:
the dumb one, **per job**.

## Decision

- **Every job runs under a keeper of its own**: a small process, standard library only, nothing of errand imported
  ( as `_keeper.py` is for a run's streams ), started by whoever starts the job, in a session of its own. It
  starts the job, holds its pipes or its terminal ( pty, pseudo-console ), and ends with it.
- **The job's directory is the record**, as today: `meta.json` ( what, when, pid, group and its start, `keep` ),
  `out`, `err`, `exit`. The keeper writes them. Beside them, **a socket** ( `sock` ) the keeper listens on.
- **The socket's protocol is small and fixed**: attach from these offsets, input, close input, resize, signal,
  stop; output and exit back. It is `frames.py`'s as it is today, between an agent and its client. With the
  directory's layout, this is what stays the same from one version of errand to the next -- a number in
  `meta.json` says which ( `v` ), and a keeper answers what it knows.
- **Agents own no job.** An agent ( host, environment ) finds jobs by their directories, reaches each through its
  socket, passes streams on, reads the table. It can be stopped, started again, replaced by one of other sources at
  any moment: nothing that runs notices.
- **`--batch` is the same path**: the job started, nobody attached, the command back at once. `errand attach`,
  `errand stop`, the table, the page: as for any run. `sbatch` stays what lets go of a Slurm job ( the scheduler
  owns it ); the `nohup` line goes.
- **A connection that closes says nothing.** Today a process that may not outlive its client is stopped when its
  last listener goes ( `Proc.detach` ), whatever made it go. From now on:
    - **stop is said**: Ctrl-C, `errand stop`, `k`, the end of the command that drives a run not kept -- an explicit
      `stop` on the socket, by whoever knows the command ended ( the command itself; the hub, which sees its
      client go );
    - **a restart is said too**: an agent that goes on purpose ( `errand agents stop`, `restart`, newer sources )
      closes its connections after saying so; the keeper waits for the next one;
    - **silence is not a stop**: a connection lost with no word ( an agent killed, a network dropped, a laptop
      closed ) leaves the job running. A job not kept whose driver never comes back ( `keep` false, nobody attached
      for `LEASE` ) is then stopped by its keeper: the one case decided by time, and said so in its record.
- **Stopping is the keeper's**: INT, TERM after 2 s, KILL after 5 s, to the whole group, then its `cancel` line
  ( ADR 0008, 0009 ). It happens whether or not anybody stays to see it.
- **A keeper killed outright** ( SIGKILL, the OOM killer ): its job's group still runs, no socket answers. That is
  the orphan, found and killed by the next agent that looks, as today.
- **Cleaning.** A job leaves one directory, removed by its keeper once its end was delivered ( or by a sweep, after
  `KEEP_DAYS` ). Agents holding nothing, a host agent starting removes **everything** of other sources on its
  machine: agents' directories, sources, hubs, bells -- without asking whether something runs there, since nothing
  does. What errand writes outside projects is listed in one place ( an inventory the sweep and a test both read ).

## Considered options

- **Every job under `nohup`, its facts in files, no keeper**: enough for a job nobody types into. A terminal needs
  a process holding the pty while nobody is connected; so does stopping "whether the client stays or not". That
  process is the keeper.
- **A dumb agent per machine, never updated**: one process whose death takes every job's owner with it; "never
  updated" holds until its first bug, and the day it changes is today's problem again, one level down; one more
  thing to clean.
- **`--batch` through today's agents** ( `keep`, as a lost connection is survived ): one path, but a three-day run
  is held by a three-day-old agent, and the cleaning rule keeps its exception.
- **The keeper inferring a stop from a closed connection**: cannot tell a kill from a restart.

## Consequences

- One more process per job ( a Python started with `-S`: milliseconds, a few MB ). Whether it and a run's
  `_keeper.py` become one is to be seen: they are at two levels ( a process an agent starts; a case of a run ).
- No performance lost to files: the socket carries the live stream; the files are for what was missed.
- Coexistence of agents is no longer what protects runs. Agents of several sources may still be up for a moment;
  none has to be kept.
- ADR 0005 ( an agent of older sources finishes its runs ), ADR 0008 ( owner: the agent ) and ADR 0024 ( older
  agents stopped with `--force`, their runs run again ) are replaced on those points.
- A command killed outright with no hub ( `ERRAND_AGENT=direct`, SIGKILL ) cannot say `stop`: its run goes on until
  `LEASE`, shown by `errand status`.

## Open

- **`Preload`**: a session is a fork of the zygote, handed the session's descriptors ( `warm.py` ). The keeper
  must be what hands them: to be read before step 2.
- **Windows**: a named pipe in place of the socket; the Job Object held by the keeper ( `winjob.py` ).
- **Containers**: the keeper inside, with the job ( the environment's Python, as the environment agent ); an agent
  that cannot detach ( `apptainer exec` ) no longer takes its jobs with it -- to be checked: the image stays
  mounted only while a process of that `exec` lives.
- **Services** ( `errand serve keep` ): jobs like others, or their own keeper as today.
- `LEASE`: a day ( Hugo, 2026-11-23 ): long enough for a laptop closed overnight; whether a run may ask for none.

## Steps

1. The keeper and its protocol, alone: `agents/keeper.py`, tested without an agent ( start, attach from offsets,
   input, a terminal, stop, a connection closed with and without a word, a keeper killed ).
2. `procs.Proc` becomes a client of a keeper; `kill_orphans` reads "no socket answers"; the table unchanged.
3. `--batch` through it ( local, ssh ); the batch record keeps only what goes together; `nohup` removed.
4. Agents let go: stop and restart without `--force` whatever runs; a host agent's start removes other sources'.
5. The inventory of what errand writes outside projects, its sweep, its test.
6. `Preload`, Windows, containers.

## Done, 2026-11-23: step 1

- `agents/keeper.py`: the keeper ( `python -S -E keeper.py DIR`, one `select` loop, no thread, nothing of errand
  imported ), and the other side in the same file ( `start`, `connect`, `Link` ). A first process says `ok` or why
  not, then goes: the keeper is nobody's child. Bound by its name from its directory ( a socket's address is short );
  reached through `endpoint.address` when the path is long.
- **A client is fed from the files**, at its own pace ( at most `AHEAD` queued ): a client that does not read holds
  nothing back, and an attach from offsets is the same code as following live.
- A connection closed stops nothing; `left` says when the last client went and its word ( `restart`, `bye`, none );
  `{"op": "set", "keep": true}` keeps a job from then on ( what `--batch` will say ); the lease stops a job not kept
  with nobody, and writes `why`. A keeper told to go ( TERM, HUP, INT ) stops its job first.
- Its record goes once its end was delivered ( `tidy` ); not delivered, it stays.
- Tests: `tests/test_keeper.py`, 12 entries, no agent in them.

Measured on the Mac ( Python 3.13 ): 28 ms from `start` to a job running; 8.5 MB per keeper at rest, no CPU;
350 MB/s from a job through its file to a client. On lmo, a bare `python -S` with the same imports: 32 ms, 12.6 MB
( 5.9 MB its own ).

Not done in this step: a job that ends before anybody attached is read from its files by who comes ( no helper
yet: step 2 ); Windows; the short links of `endpoint.address` ( `/tmp/errand-<uid>/` ) are only removed when another
is made ( for the inventory, step 5 ).

## Done, 2026-11-23: step 2

- `procs.Proc` is a choice: `KeptProc` ( POSIX ) starts the process under a keeper and is one client of it -- it passes
  on what the keeper says to the agent's own clients, from the offsets each has; `PipeProc` ( what `Proc` was ) stays
  for Windows and behind `ERRAND_KEEPER=0`. The job's words ( `cancel`, `waiting`, `leaf` ) stay in the agent's marker
  ( `meta.json`'s `mark` ), read there by the keeper and by the table.
- **An agent that finds a job running under its keeper follows it again** ( `KeptProc.adopt` ): at its start, those of
  its own directory ( `procs.sweep` returns them; one that may not outlive its client is stopped: its client was
  reached through the agent that is gone ); at an `attach`, one whose record it finds ( another directory's, other
  sources' ) instead of replaying it as lost and removing its record.
- **An orphan is a job no keeper answers for** ( `procs.kept`, `keeper.answers` ): `kill_orphans` leaves the others;
  the neighbours' sweep ( `endpoint.sweep` ) never removes a directory where something runs; the table says `running`
  for a job whose keeper lives, its agent there or not ( `agent_alive` says which ), and a keeper is nobody's stray.
- A keeper killed outright while its agent is there: the agent ends what it held and says so ( exit -9 ).
- **A keeper whose record is removed under it stops its job** ( looked at every `WATCH` = 15 s ): found by a test
  that removed its scratch directory and left a keeper and its job running for 20 minutes.
- Unchanged in this step: an agent stopped on purpose ( `stop --force`, SIGTERM ) still stops what it runs; a process
  not kept still goes with its last client. `--batch` still goes by `nohup`.
- Tests: `test_agents.py` ( an agent killed: the job goes on and the next agent follows and stops it; agent and keeper
  killed: the next agent kills what is left, and cancels its job; a keeper killed under its agent ),
  `test_status.py` ( running under its keeper, orphaned once both are gone ), `test_keeper.py` ( 13 ). The whole suite
  passes ( 20 skipped: no `ssh localhost` here, no numpy ).

Seen on the Mac: a run through the `jax` environment agent ( micromamba ), its keeper 9.5 MB, nobody's child; the
environment agent killed with SIGKILL at tick 15 of 45: the job went on, `errand status` said it running, the command
that drove it ( `ERRAND_AGENT=direct` ) reached a new agent by itself, which followed the job again; every tick shown,
`PASS 45.2 s`, its record removed. Not seen: over ssh, in a container, under `Preload`, through the hub.

## Done, 2026-11-23: step 3

- **`--batch` through an environment is the run that is waited for, let go of** ( `cli/drive.submit`, when
  `Reach.detachable()` ): prepared, then a session started by the environment's agent, kept, under its keeper; its
  channel closed at once. Place: `{ kind: agent, handle: <the session's id>, live: <what attach reaches it by> }`.
- **Its id is chosen here** ( `agents.start( ident = … )`, the agent's `spawn` takes `id` ) and recorded before the
  request goes ( `launching`, `how: agent` ): a connection lost on the way is settled by asking the agent whether it
  knows that id ( `batch.resolve` ), as the pid written over there settled it for `nohup`.
- **One record to attach by**: the `live` record a run that is waited for leaves ( `_remember` ), marked `batch`: not
  under "left running" in `errand status` ( it is with the submissions ), followed by `errand attach ID` -- the
  submission's id too --, removed when the submission is settled or forgotten.
- Whether it runs: asked of its agent ( `list`, `batch._agent_said` ); a machine that does not answer is `cannot say`,
  once per command, as before. `errand stop ID`: its session's id among the processes of the tables, stopped as any.
- What stays as it was: `sbatch` for a scheduler; this errand detached, for an environment with nothing to go
  through; `nohup` for a container, whose agent goes with the command that entered it ( open, above ).
- `registry.note_root` at a submission: its project's runs are in the page's tree ( it was missing: the outputs of
  `guitar_2000`'s two runs were never listed ).
- Docs: `guide/detached.md`, `guide/status.md`, `guide/agents.md`. Tests: `test_batch.py` ( through a local
  interpreter layer: in the table with its keeper and its project, `status`, `stop` by the submission's id; ends,
  settled, nothing left to attach to ), `test_unreachable.py` ( the fake machine over ssh: submitted, `cannot say`
  while it does not answer, found and brought back; waited for, then submitted ). The whole suite passes.

Seen on the Mac, through the `jax` environment: `errand run --batch` back in 0.8 s; from `~/Maison/guitar_2000`,
`errand status --mine` counted it among what runs ( `Projects: 2 running on Mac` ); `errand attach 3318` showed its 25
ticks and `PASS`; its `live` record and its keeper gone after. Not seen: on lmo or any real machine over ssh ( the
suite's ssh tests are skipped here: no `ssh localhost` ), `errand web` itself ( it reads the same table ).

## Done, 2026-11-23: steps 4 and 5

**Agents let go ( step 4 ).**

- `Agent.leave()`: the agent goes on purpose; what may outlive its client is released ( `KeptProc.release`: the keeper
  is told `restart`, and the agent's follower ends without a word about the run ); what may not is stopped, its
  client being reached through this agent. `stop` unforced, a SIGTERM, a retirement: `leave`. `stop --force`: as
  before, the runs first.
- `can_leave()`: not an allocation's agent ( its job ends with it ), not a container's ( `attached-…` ), not while a
  process is held by the agent itself ( Windows, `ERRAND_KEEPER=0` ): those answer as before -- left running, unless
  forced.
- **Other sources retired** ( `endpoint.retire_others`, by this machine's host agent at its start, then again with a
  growing wait while something of theirs is there ): each agent of other sources is asked `retire` -- it goes once
  nobody follows a run through it --, one too old to know it `stop`, which it refuses while it runs something; a
  directory nobody serves and under which nothing runs is removed at once, whatever its age ( it was: after an hour,
  and a week for a session kept ). A directory a run still leaves its words in stays until that run ends.
- A keeper removes the marker its job wrote its words in, when it ends.
- The copies of errand's sources: one nothing names goes after a day ( two weeks on a shared machine, where a job may
  wait in a queue ), never one a process runs from ( `bootstrap.copies_running` ).
- `errand agents stop` says `stopped; N runs of its go on, under their keepers`.

**The inventory ( step 5 ).** `errand/inventory.py`: every name in errand's per-user directory and every place
elsewhere, with who removes it; `unknown( cache )` says a name nobody declared ( never removed ); `sweep()` removes
what is declared as leftover -- projects that are gone in `roots.json`, the short links to directories that are gone,
a queue directory that is not the one in use and is empty, what nothing writes any more --, from the host agent's
hourly round. A test reads errand's own sources for the names they give that directory: one not declared fails it.
It found one: `views/` ( a page's view copied where its services run, ADR 0027 ), which nothing removes -- declared
as such, to be decided. A settled `--batch` submission's kept output over there is removed too ( the agent's
`forget` ).

Seen on lmo ( over ssh, 2026-11-23 ): a submission from a scratch project listed by `errand status` typed in
`~/Maison/guitar_2000`; its environment agent killed with SIGKILL at 10 s: still listed running; `errand attach`
showed ticks 0 to 49 and `PASS 50.1 s`; results back, nothing left to attach to, no keeper. lmo's agents: 13
directories before, 3 after ( this version's ); its copies of the sources: 185 ( 625 MB ) before, 16 after. The
`ollama` service there stopped by itself meanwhile ( `idle for 2 min`, its own rule; its log says so ), and its
agent, of lmo's own errand, refuses a `stop` while it runs.

## Step 6: where it stands

- **`Preload`**: nothing to change -- the session that asks the zygote for a fork is the keeper's child, and hands
  the fork the keeper's pipes; `test_warm.py` passes as it is. Not seen with a real `torch` session.
- **Containers** ( done, 2026-11-23 ). Tried on lmo ( apptainer 1.5.1, `jax-cuda.sif`, a FUSE mount ): a process left
  inside `apptainer exec`, in a session of its own, loses the image 1.5 s after exec's own process ends ( `Terminating
  squashfuse_ll after timeout`, then `Transport endpoint is not connected` ). So nothing can outlive the command that
  entered a container, and a keeper inside one would not help. The other way round: **the keeper stays outside, and
  its job is the command that enters the container** -- `--batch` through a container is the whole line of the
  session ( `Reach.line`: `cd … && apptainer exec … python -m errand.agents.ops session …` ), started by the agent
  outside it ( the machine's: `ssh_target`, or this machine's host agent ), kept. No `nohup` is left anywhere in
  `--batch`. The table says such a session as any ( its request is read out of its line ). A run that is waited for
  in a container goes as before, by the container's own agent ( entered once, warm ): it is followed for as long as
  its command is there.
- **A stop gave what a leader started no time** ( found there ): the leader gone at the first signal, the rest of its
  group was killed at once -- `apptainer`'s runtime parent with it, in the middle of unmounting: one `squashfuse_ll`
  left for ever per container agent stopped ( two on lmo since 15 and 16 November ). The keeper now gives the group
  the rest of the stop ( TERM, then KILL at 5 s ) and ends as soon as nothing of it is left ( `Keeper.reap` ).
  Seen on lmo: a waited run and a `--batch` one in the container, no `squashfuse_ll` more after either; the
  `--batch` one with its host agent killed with SIGKILL at 5 s: `errand attach` showed `Debian GNU/Linux 13` at its
  first and last line, 40 ticks, `PASS`.
- Found on the way, not errand's to decide alone: on lmo `~/.cache/errand` is a link to `/data/errand-cache`, which a
  container does not see without `flags = [ "--bind", "/data" ]`; errand says `can't open file …/_boot.py`, not why.
- **Windows**: unchanged, and not tried: no keeper there ( `PipeProc` ), an agent refuses to go while it runs
  something. To do: a named pipe for the socket, the Job Object held by the keeper.

## The keeper in Go ( 2026-11-23 )

`go/keeper` ( see `go/README.md` ): the same record and socket, standard library only. Started in place of the Python
one where `ERRAND_KEEPER_BIN` names it. It passes `test_keeper.py` as it is ( 14 tests ) on macOS arm64 and Linux
amd64, and `test_agents.py`, `test_batch.py`, `test_status.py` on macOS: the contract of this ADR is enough for
another implementation. Measured on the Mac: 11 ms to a job running ( Python: 28 ), 5.9 MB at rest ( 8.5 ),
555 MB/s ( 350 ). Not done: shipping the binary to another machine; Windows.

## The agent in Go ( 2026-11-23 )

`go/errand` is now one program: `errand-go keeper` and `errand-go agent` ( `go/README.md` ). The agent does what
concerns processes itself -- with keepers there is little: it holds no run, each channel that follows one is its own
connection to the keeper -- and relays every other operation, as it is, to an agent of errand's Python kept beside
it ( `DIR/py`; `Agent( root, keep, at = … )`: it listens there, and is the agent of `DIR` for the rest ). So the
port goes one operation at a time, with everything working in between.

Started where `ERRAND_AGENT_BIN` names it. `test_agents.py` passes as it is; the whole suite passes but two tests
that pass by themselves with either agent.

Found by it, in what this ADR had left unsaid:

- a job that names no directory ran in its keeper's ( its own record ), not where its agent was: a keeper now
  remembers where it was started from;
- a keeper went while a client that had just connected had not yet said it wanted to attach, and the client got the
  end without the output: a keeper waits for a connection that is open, and an agent sends from the record what a
  keeper did not;
- a process that started and ended between two looks of the agent was never said to end to who watches.

## The table and the subscription in Go ( 2026-11-23 )

`go/errand` reads by itself what runs on its machine ( `table`, `seen`, `watch` ): see `go/README.md`. Checked against
errand's Python, field by field, on a scratch machine ( `tests/test_go.py` ) and on the Mac's real state. Still
Python's in a table: the list of the services ( their declarations ), asked of the Python beside when a file of
theirs moves; and any machine with a VM errand boots or an allocation held warm, whose table is relayed whole.

One thing changes for who watches: an agent that owns no pipe does not see a run write. A watcher is told a process
wrote because its files grew, looked at twice a second while somebody is subscribed.
