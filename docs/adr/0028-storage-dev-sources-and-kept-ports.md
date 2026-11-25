---
status: accepted
date: 2026-11-22
amends: 0022, 0027
---

# File systems are what errand looks after; services have versions to clean, sources to develop, ports to keep

ADR 0027 gives each service a version computed from its files (`blender#63ecc287`), and fetches the code of that version
from the repository at the store's commit. It works, but using it for a day showed four problems:

- **versions pile up.** Each commit that touches a service makes a new version, with its declaration, its keeper's
  directory, its env, and a fetched repository. On Hugo's Mac, after two days: three blender declarations and 265 MB of
  fetched repositories for two commits of sdot. Nothing says which ones are still needed, nor removes them;
- **developing a service is painful.** Every edit of `blender.py` makes a new version: commit, push, write the store again,
  fetch, declare. What one wants while working on it is the checkout one is editing;
- **errand has no notion of a disk.** The caches know theirs (`errand-caches.id`, ADR 0022), but the copies of a project,
  the fetched code and the service envs live on disks errand does not name. The same home seen by lmo and n22 is two
  places for everything except the caches;
- **a page cannot be reopened.** A bookmark of `errand web`, or of a store, finds nothing once the host agent has stopped:
  nothing listens on its port, and a browser cannot start anything. A service reached through `errand serve forward` (Ollama
  on lmo, from the family server) has the same weakness on the side of the forward: its port is held by a process in a
  terminal. Yet errand must leave nothing running on a shared cluster, and nothing in the background without consent.

Settled in a grill on 2026-11-22 (`grill/services-dev-storage/decisions.md`, two rounds).

## Decision

### 1. Directories have an identity; mounts group them

Machines share some directories and not others: on a cluster, the home is one NFS export for every node, while `/scratch`
and `/tmp` are each node's own; a VM shares only what is bound into it. So sharing is decided per directory, never per
machine nor per "home".

**The certainty: an id per errand directory.** Every directory errand makes or uses gets once a small id file: `{ id,
made, host }`, a random id, the time, and the machine that wrote it. ADR 0022's `errand-caches.id` is one; this ADR adds
`~/.cache/errand/dir.id` (errand's own per-user directory), the id in each project copy's manifest (`.errand-copy`), each
service env (`svc-envs/<name>@<version>/errand-dir.id`) and each checkout declared as a dev source (`.git/errand-id`:
inside `.git`, invisible to git and to the project). Two machines that read the same id in a directory share that
directory, whatever its neighbours do. It is the only test errand trusts.

**The grouping, and a guess before any id: the mount.** For each such directory, an agent reads the mount that holds it
(Linux: `/proc/self/mountinfo`; macOS: `statfs`, as `df -P` shows it): its source, its type and its mount point. Not
`fstab`: it says what is meant to be mounted, not what is. A mount is named by its source when it is a network one
(`nfs:server:/export/home`, `lustre:…`, `smb:…`), else by the machine and its device (`node14:/dev/nvme0n1p1`, `tmpfs`
included). The same network source seen from two machines is shown as *probably shared* until an id confirms it; a local
mount is never shown as shared.

**What hangs off a directory** follows it: its caches (ADR 0022), the copies of projects (ADR 0006) with where they come
from (§3), the service versions with their envs and fetched code (§2), and the global configuration (`~/.config/errand/`):
what it declares holds for every machine that reads that same configuration directory, and only where the paths it names
are the same directories (§3).

The `caches` tab of `errand tui` and `errand web` becomes **`storage`**: one row per mount, with the machines that see it
(and, on a cluster, `home · nfs · 12 machines` above `scratch · node14`, `scratch · node15`), its size and what can be
reclaimed; unfolded, the directories errand has on it: its caches (the tab as it was), its copies, its service versions
and envs, its dev sources. A directory whose id two mounts show (a bind into a container) is shown once, under the mount
it is made on. The caches' keys and rules are unchanged; `errand caches` stays as the command for the caches.

### 2. Service versions are tracked and cleaned

A service version (`blender#63ecc287`) brings with it its declaration, its keeper's directory (`serve/<key>/`), its env
(`svc-envs/<name>@<version>`) and a share of the fetched code (`repos/<host>/<path>/<rev>/`, shared between the versions
of one commit). Per file system:

- **used**: asked by a store still present among the views registered on that disk (`static-views.json`, ADR 0017), or
  run within the last N days. N = 14 by default, as `--unused` for the caches;
- **purged** by the host agent once a day, as the caches are: an unused version goes with its declaration, its keeper's
  directory and its env; fetched code goes with the last version that needed it. A version running is never touched;
- **pinned**: `errand serve pin NAME#VERSION` (and `unpin`, and a key in `storage`) keeps one out of the purge;
- **seen** in `storage`, under its file system: per service, its versions, each with its state, its size, its last use, the
  stores that ask for it (said missing when gone), whether it is pinned; and a key to remove it;
- **fetched smaller**: a service that does not need its package's Python (blender) gets only its own directory at the
  store's commit (`src.dir`, a few hundred KB), through a sparse checkout, instead of the whole repository.

`errand serve list --versions` and `errand serve prune [--unused DAYS] [--dry-run]` do the same on the command line.

### 3. Dev sources: a checkout one works on, declared per file system

In the global configuration, one says that on this disk a repository is represented by a checkout:

```python
# ~/.config/errand/services.py
errand.dev_sources[ "github.com/sdot-team/sdot" ] = errand.Checkout( "~/Projects/sdot-ffi", env = "prj-jax" )
```

When a store asks for a service of that repository (its `source.git`), whatever its version, the service can run from the
checkout: blender with the checkout's `blender.py`, `sdot.region` in `prj-jax` where sdot is installed in editable mode. It
is not checked against the version the store asks for: it is the checkout as it is. Its keeper restarts it when the
service's files change in the checkout (their mtimes, read at each connection), so that an edit is seen at the next
picture.

**Only where it is that checkout.** The global configuration may be read by machines that do not see the checkout (a
scratch path, a home not mounted there): a machine uses a dev source only if the path exists there and carries the
checkout's id (`.git/errand-id`, §1); elsewhere it is ignored, and the panel says why when it matters.

**Copies carry it.** A push of a project (ADR 0006) now writes in the copy's manifest (`.errand-copy`) where it comes from:
`origin: { machine, id, path }` (the id of the source directory), and `synced`: when. A copy of a checkout declared as a dev source is a dev source on the
disk where it lives, for the same repository, without a line written there: `lmo:~/Projects-rsync/sdot-ffi`, copy of
`Mac:~/Projects/sdot-ffi`, serves the services of sdot on lmo. The env is the one the machine's errand environment names
for that project (`lmo-jax`); without one, the service cannot run in dev there, and the panel says so.

**A copy can be late.** The panel and `storage` say when it was last synced; a key in `storage`, and
`errand serve sync MACHINE [PROJECT]`, push it again.

**Seen and changed in `storage`**: per file system, its dev sources, declared or carried by a copy, each with its
repository, its path, its env, its last sync, and an on / off switch. The switch writes a line in the configuration of
that disk (`dev_sources_off`), never in a store.

### 4. The page chooses between the store's version and the checkout

When a dev source exists on the machine chosen for a service, the Services panel offers, per service, as for the machine:

- **"this page's version"**: `blender#63ecc287`, of commit `c6e90e9`;
- **"your checkout"**: `~/Projects/sdot-ffi`, last changed 3 min ago, with a `dev` badge on the service, on Render, and on
  the picture shown.

The choice is kept per view, by errand (`POST .errand/services/<name>` takes `{ "on": M, "from": "version" | "dev" }`), as
the machine is. The page adds no source: it only chooses among those declared. **By default, the checkout**, when there is
one: it was declared to work on it. The panel holds a short help that says what the two are, and, when there is no dev
source, the line to write to have one.

`.errand/machines/<M>` gives, per service, `dev: { repo, path, env, changed, copy_of, synced }` when a dev source exists
there, and the state of the service from it (`ready`, or why not: no env, the copy late).

### 5. Dev stays local

A store carries no trace of the dev sources of the machine that wrote it or viewed it: elsewhere, only that machine's
declarations count. A picture rendered from a checkout and kept in the store (Keep) is marked `dev` in
`renders/index.json`, with its base commit and "local changes", so that nobody takes it for a picture of the version.

### 6. Ports kept by the system: nothing runs between two uses

macOS (`launchd`) and Linux (`systemd --user`) can listen on a port in a program's stead and start the program at the
first connection (socket activation). Between two uses, no process of errand runs: only the system holds the port.

- `errand web keep` keeps the ports of the page and of the stores (20501, 20503): a bookmark wakes the host agent, which
  stops again after its usual idle time;
- `errand serve forward NAME --host H --keep` keeps the port of a forward: a connection wakes the agent here, which opens
  the channel to H, whose agent wakes in turn (ADR 0018);
- `--off` removes what was installed. `errand status` and `storage` (under the machine's file system) list the ports
  kept this way.

Never installed unasked. One learns it exists when it matters: `errand web` says once "this page goes when errand stops;
`errand web keep` keeps its address", the page says it at its foot while `keep` is not installed, and `errand serve
forward` says "Ctrl-C closes this port; `--keep` keeps it". No question asked.

Windows has no socket activation: `keep` is not offered there for now, and says so.

### 7. Shared machines

A machine is **shared** when its errand environment says `shared = True`; a Slurm place and the login node of a cluster are
shared without saying it. On a shared machine:

- an agent stops after 2 min without a client (15 min elsewhere, as now);
- a service stops after 10 min idle at most, and after 2 h in all, whatever it declares;
- `keep` is refused;
- `errand status` shows in red what is left running there.

Elsewhere, nothing changes. There is no list of machines allowed to keep things: what stays is what a person asked to
keep (§6), and a cluster keeps nothing.

## Considered options

- **One id per user and disk (`fs.json` in errand's directory).** The first version of this ADR. On a cluster, the home
  is shared and the scratch is not: one id would have put every node's scratch on the home's file system.
- **The mount alone, from `/proc/self/mountinfo` or `fstab`.** Good to group and to guess, not to decide: a bind, an
  automount, two exports of one volume, a path mounted under another name, all fool it. An id read in the directory
  cannot be fooled; the mount only says where to show it, and guesses before an id exists.
- **Keep every version until removed by hand.** 265 MB in two days; and a person removes nothing they cannot see.
- **Dev sources in the project's `errand-envs.py`.** They describe the machine's disks, not the project: two projects
  using sdot would each have to say where its checkout is, and a store opened outside any project would know none.
- **Dev sources in the store.** A store would then name paths of the machine that wrote it: what ADR 0027 removed.
- **A dev source as a hand-made declaration of each version** (`errand serve install … -- my checkout`). It works today,
  and is what Hugo did: a new declaration at each edit, "unverified" each time.
- **An agent kept alive (`resident`) on personal machines.** A process in the background for the occasional bookmark; the
  system's socket activation does the same with nothing running.
- **A list of machines allowed to keep processes.** To be kept up to date by hand; consent per port asked (§6) is closer to
  what a user expects, and clusters are recognized without a list.

## Consequences

- A directory shared by several machines is one thing in errand, and one that is not shared is never taken for it: a
  cluster's home is shown once with its nodes, each node's scratch apart.
- Service versions no store asks for go within two weeks; what they cost is visible before.
- Working on a service no longer goes through commits: the page uses the checkout, says so, and pictures made from it say
  so too.
- A bookmark of `errand web` or of a store, and a forward, work after errand has stopped, when the user asked for it once.
- On a cluster, errand leaves less behind than before.
- Two new files errand writes outside its cache: the launchd / systemd units of `keep`, under the user's own directories
  (`~/Library/LaunchAgents/`, `~/.config/systemd/user/`), removed by `--off`.

## Steps

1. **Directories and mounts**: the id files of errand's directories (its own, copies, envs, checkouts; caches as ADR 0022);
   the mount of each read by the agents (`look`: mountinfo, statfs), sources compared across machines; the `storage` tab
   (tui, web): mounts, then their directories, the caches under them.
2. **Versions**: usage (stores asking, last run), `serve list --versions`, `pin` / `unpin`, `prune`, the daily purge by the
   host agent, versions in `storage` with removal; the sparse fetch of a service's directory.
3. **Dev sources**: `errand.dev_sources`, `errand.Checkout`; the keeper running a service from a checkout and restarting
   it when its files change; `origin` and `synced` in `.errand-copy`; copies as dev sources; `serve sync`; dev sources in
   `storage` with their switch.
4. **The page** (sdot's viewer): the choice per service, the `dev` badges, the help; `.errand/machines/<M>` with `dev`;
   pictures kept marked `dev`.
5. **Kept ports**: `errand web keep`, `forward --keep`, `--off`, launchd and systemd units; the messages; listed in
   `status` and `storage`.
6. **Shared machines**: `shared`, implicit for Slurm and login nodes; the limits; red in `status`.
7. The guide (*storage*, *developing a service*, *keeping a page reachable*) and tests for each step.

## Progress

- 2026-11-22, step 1 done (uncommitted):
  - `errand/storage.py`: `dir_id` ( an id file per directory: `dir.id` in errand's own, `.errand-copy.id` at a copy's
    root, `errand-dir.id` elsewhere ), the mounts ( `parse_mountinfo`, `parse_mount` for macOS, drive letters on Windows ),
    `mount_of` by device ( macOS' firmlinks: `/Users` is on `/System/Volumes/Data`, one device with `/` ), `key_of`, the
    directories ( errand's own, copies, service envs, fetched code ) with sizes measured within 2 s each, `look`.
  - The agent that receives a sync notes the copy ( `storage.note_copy`, `<cache>/copies/` ); the `caches` op answers
    `storage` too, and each cache directory says its mount.
  - The tab ( `tui/caches.py`, kept as `caches` inside, shown as `storage` ): a row per mount, then errand's directories,
    then the caches as before under their mount. Two findings on the real machines changed the plan: a disk a server
    exports is one row ( cinaps' `/workdir2`: xfs there, nfs on node14, one cache id read from both: mounts that show one
    directory are merged, named after the server's own disk ); a mount with nothing of errand's ( the home's alone ) is
    not shown. An agent that says no mounts ( older ) has its caches shown as before.
  - Checked in `errand web` on Mac, lmo, cinaps and node14. Tests: `tests/test_storage.py` ( 4 ), two in
    `tests/test_caches.py`; `test_syncop` allows the copy's id. Checkouts' ids ( `.git/errand-id` ) come with step 3.
- 2026-11-22, step 2 done (uncommitted):
  - `errand/versions.py`: `inventory` ( declarations, keepers' directories, envs, fetched code by commit; the stores
    that ask for each among the views registered here; last use from the keeper's files, the declaration, the env ),
    `remove` ( refused while it runs ), `prune` ( 14 days ), `pin`, `prune_daily` called by the host agent's hourly round.
  - `caches_remove { version }` on the agent; the `caches` answer carries `storage.versions`. In the storage tab, a
    `services` row under the mount of errand's directory, a row per version and per fetched commit, unused ones marked;
    `d` on a version asks, then removes it.
  - CLI: `errand serve list --versions`, `pin`, `unpin`, `prune [--unused DAYS] [--dry-run]`.
  - Sparse fetch: `git:URL@REV#paths=A,B`, the paths from the service's directory and its `version_of` ( blender needs
    `../store.py` ); sdot's services at c6e90e9: 84 KB instead of 138 MB, the same version computed. A commit fetched
    for some paths gets others added later.
  - Tests: `tests/test_versions.py` ( 2 ), one in `test_caches.py`; `test_install`'s line has `#paths=`.
  - Found: sdot's viz tests leave their stores in the system's temporary directory, registered as views: 224 of them
    on Hugo's Mac, which keep old versions "asked for". The tests should remove what they write.
- 2026-11-22, step 3 done (uncommitted):
  - `errand.dev_sources`, `errand.Checkout( path, env, envs )` ( `serve.py`, exported ); read from the user's file by
    `page_services.machine` ( `machine_dev` ).
  - `errand/dev.py`: `sources` ( declared, then carried by copies through `.errand-copy.origin` ), the checkout's id in
    `.git/errand-id`, `match` by `source.git`, `look` ( for the diagnosis: state, why, last change ), `ensure` ( `NAME#dev`
    composed from the checkout's own description, declared again when the command or the `version_of` files' mtimes
    change, the running one stopped ), `set_off` ( `dev-off.json` in the user's configuration ), `origin_for`.
  - The diagnosis adds `dev` per service; the page's choice `from` ( `dev` / `version` ) kept per view by errand
    ( `from.json` ), `dev` by default when usable; the relay ensures `NAME#dev` here, or asks M's agent
    ( `services` op `dev_ensure` ) and forwards to it.
  - A push sends `origin` ( machine, path, the dev sources inside the project ); the receiving agent writes
    `.errand-copy.origin`. The storage tab lists dev sources under their mount, `d` turns one off or on.
  - Checked in a browser on the seeds store behind an isolated errand with `~/Projects/sdot-ffi` declared: the three
    services from the checkout ( `prj-jax` ), a region cut through `sdot.region#dev`, an edit of `region.py` seen at
    the next connection ( new signature, new process ), a Blender picture from the checkout's `blender.py`.
  - Tests: `tests/test_dev.py` ( 3 ), a row in `test_caches.py`; `test_syncop` allows the origin file.
  - Not done: the page's own choice and badges ( step 4: the panel still shows only the state ); a real copy on lmo
    carrying a dev source ( needs a push after the declaration in Hugo's own file ).
- 2026-11-22, step 4 done (uncommitted), in sdot's viewer:
  - per service, a choice "this page's version" / "your checkout" ( `POST .errand/services/<name> { from }` ), with the
    checkout's path, env and last change in its title, and a line saying what each runs; a `dev` badge by the name;
    Render says `Render · dev`, outlined, and the picture's card "dev: your checkout"; the version's own sentence hidden
    while the checkout runs it.
  - When no dev source exists and the store names a repository: "Working on sdot?" and the line to write.
  - `renderRequest` sends `dev: { checkout, base }`; the Blender service's Keep writes `dev: { base, local_changes }` in
    `renders/index.json`, and the page's kept pictures say `dev`.
  - `.errand/services` now gives each service its `source` ( repository, commit ).
  - A dev source turned off in the storage tab makes pages run the store's version whatever was chosen ( `_with_dev` ).
  - Checked in a browser behind the isolated errand: the three services from the checkout, the switch to the version and
    back, a Keep marked `dev` with base `f494498`, the source turned off. Tests: `test_dev.py` has the off case.
- 2026-11-22, step 5 done (uncommitted):
  - `errand/keep.py`: launchd plists ( `Sockets`, `AbandonProcessGroup` ) and systemd `.socket` + `.service` units
    ( `FileDescriptorName`, `KillMode=process` ) under the user's directories, `install` / `remove`, `kept.json`,
    `inherited( name )` ( `launch_activate_socket` through ctypes; `LISTEN_FDS` / `LISTEN_FDNAMES` ), `refused` ( Windows,
    a shared machine: step 6 fills `machines.shared` ).
  - The page: the system keeps the PUBLIC ports and starts a relay ( `errand/web/kept.py` ) that ensures the host agent
    and pumps each connection to it; the agent, seeing `kept.json`, listens on ports of its own and says the public ones
    ( `public` in `web.json` and `static.json`, the URLs, the Host / Origin checks, the cookie's name ). So the agent can
    stop, restart or hand over while the bookmark works; the relay goes after 5 min without a connection. Chosen over
    handing the socket to the host agent itself: an agent handing over to newer sources would have had to pass the
    system's socket along.
  - `errand web keep` / `errand web keep off`; `errand serve forward NAME --host H --port N --keep` ( `--off` ), the
    forward taking the system's socket ( `--kept` ) and going after 10 min idle; `errand status` lists what is kept;
    the messages: once at `errand web`, at the page's foot while not kept, in `forward`'s line.
  - Checked: on the Mac, a launchd unit end to end ( the relay, an agent behind on 53152 saying 28601, the agent
    stopped, the same address answering again in 0.4 s ); on lmo, a systemd user socket woken twice. Test units
    removed after. Tests: `tests/test_keep.py` ( 3 ); `test_web` passes ( the message said before the address ).
  - Not installed for Hugo: `errand web keep` is his to type.
- 2026-11-22, asked on the way ( Hugo: working on errand meant forced restarts that killed runs ): a host agent of
  older sources now hands its page over at once ( agent op `give_page`: the page and the stores' server closed, never
  taken back by a `ping`; `take_page` for `errand web keep`, the same agent behind the system's ports ), keeps its runs
  and goes once they end; `web._retire` asks `give_page`, then `stop` ( never forced ). With `keep`, even the page's port
  does not move. Test in `test_web.py` with a real busy host agent. Taking runs over from an older agent ( adoption
  rather than waiting for them to end ) would need its own ADR: a process keeps its parent.
- 2026-11-22, step 6 done (uncommitted):
  - `errand/machines.py`: `shared()` ( `ERRAND_SHARED` 1 / 0, else Slurm there: `sbatch` / `squeue` on the PATH or a
    Slurm job ), the limits `AGENT` 120 s, `SERVICE_IDLE` 600 s, `SERVICE_MAX` 7200 s. Decided by the machine's own
    agent: nothing on the driving side needs to know, a flag forgotten there leaves nothing behind.
  - `Ssh( …, shared = True | False )`: the agents errand starts there get `ERRAND_SHARED` ( host agent: `export` in its
    line; environment agents: the `Command`'s env ), and the driving side keeps their connection 2 min.
  - The agent shortens its own `keep_alive`; the service keeper caps `idle` and stops after 2 h; `keep.refused`; the
    agent's `table` says `shared`, and `errand status` prints in red the services and the agents still up there.
  - Checked: node14 says shared ( Slurm ), lmo and the Mac personal. Tests: `tests/test_machines.py` ( 3 ); test_agents,
    test_serve, test_status, test_alive, test_keep, test_web pass.
- 2026-11-22, step 7 done (uncommitted): guides `web.md` ( *Storage*, *Keeping its address* ), `serve.md` ( *Developing
  a service*, *Versions pile up, and go*, `forward --keep` ), `agents.md` ( *On a shared machine*, restarting without
  killing runs ), `storage.md` ( *Which machines share what*, the per-user directory's new files ), sdot's
  `visualizer.md`; `errand serve`'s help and the TUI's keys say `storage`. The whole errand suite ( 58 files ) and sdot's
  viz tests pass, but for `test_warm`'s zygote test, which fails the same with the last commit's code while a run holds
  the machine's slot ( the queue gives the zygote no room ): the machine's state, not this ADR. `_retire` keeps `stop`
  alone for an agent of these sources said older ( `test_web_run_link` ).

