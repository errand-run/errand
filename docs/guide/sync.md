# Smart sync

Before a run on another machine, the project is there. After it, what you asked for is here. In
between, you copy nothing by hand, and nothing you care about is lost on either side.

- **Only what changed crosses.** Each side keeps a manifest of its files; a push sends the
  difference, compressed, through the connection errand already holds. Nothing changed: under a second.
- **Through every layer.** The same sync reaches a server over ssh, a Slurm node, a container, a
  Windows VM, a Windows machine. No rsync, no scp, nothing to install over there.
- **What errand did not write is never lost.** The copy follows the project, but a file over there that
  errand did not put there is neither removed nor replaced: the push stops first, and names it.
- **Results come back as they are written.** A run's record when it ends, the directories you name
  after each command, and the files it [shows as it goes](./steps#view-a-file-that-evolves) at once.
- **Caches go both ways.** Downloaded data and memoized results reach every machine, computed once.
  → [Data and results, computed once](./caching)

| on lmo, from a Mac | sync |
|---|---:|
| 2597 files, 407 MB, cold | 20.9 s |
| the same, nothing changed | 0.73 s |
| one file changed | 0.78 s |

## How the project crosses

The push is a **sync by manifest**. Each side keeps a manifest of its files ( size, mtime, hash ).
The local side sends its own, the agent over there answers with what it lacks, and whole files
cross, compressed. The manifest of the copy is kept in the copy ( `.errand-copy` ). So two hosts
that share a file system ( a cluster's login and compute nodes ) share it too, with nothing to
declare. The agent walks the copy when it starts and when it goes, and trusts the manifest in
between.

Every copy goes this way: the push before a run, `errand setup`, an image built on another machine
( `build_on` ), the `also` checkouts, the pulls of results, zones and views. There is one set of
rules, and nothing to install: on Windows, errand needs neither MSYS2 nor rsync.

A changed file crosses whole: a large file that changes a little ( a checkpoint, a dataset appended
to ) is sent again in full. Keep such files in a zone ( `back`, `cache` ) or out of the project.

## What gets pushed

The project, without the obvious. `.git`, `__pycache__`, `*.pyc`, `.mypy_cache` and
`.pytest_cache` are no part of a copy. `errand-outputs` ( at any depth ), `build`, `dist`, `*.so`,
`*.o`, `node_modules`, `.venv` and `*.egg-info` are written over there and left alone. So is what
the project's layers build there, by its exact path ( an image with its `.new`, `.prev` and
`.part` ).

`errand-outputs` is excluded both ways on purpose: each side's records, plans and builds are its
own. The only runs you get back are the ones you asked for.

## The copy follows the project, carefully

The copy over there is kept identical to the project here, within what errand wrote:

- **A file errand wrote there and the project no longer has** goes, without a word ( `-v` lists them ).
- **A file errand did not write**, or that something changed since errand wrote it, is never removed nor
  replaced. A push that would do it stops **before it does anything**, and names the files:

  ```text
  errand: the project could not be pushed to lmo:~/proj: 2 files there are not errand's, and the push
  would lose them: nothing was sent, nothing was touched
      notes/theirs.txt: not written by errand ( 2026-11-25 09:12:40 )
      config.json: changed there since errand wrote it ( 2026-11-25 10:03:11 )
  ```

  The first is something errand never put there; the second is an error to look at: a run changed a file of the project, and it
  was not brought back ( a file that was, by `--pull` or a `back` zone, is no obstacle ). They are yours to keep or to `rm` there; a path a run writes is declared a [zone](#zones); or
  `errand run --force` moves them aside, into `.errand-copy-kept/<when>/` of the copy ( kept 30 days ), and goes on.
  What a directory held before errand came is never errand's: a file there that is the same as here is left as it is, and no
  obstacle until it would be replaced or removed. What another
  project's push wrote, in a copy that holds this one or that it holds, is errand's too.
- **A directory a run made** ( a build, a collected catalogue ) is not errand's: a push says it
  once, with what it holds and how to zone it, then leaves it. It is removed only once it is empty.
- **A directory tagged as a cache** ( a [`CACHEDIR.TAG`](https://bford.info/cachedir/), as pytest,
  cargo, ruff and loom write ) is left alone on both sides, without being declared.

## Checking: `errand check`

What errand believes of a machine, against what is there:

```text
$ errand check lmo
lmo
  ✗ /data/proj: a push's lock that is nobody's ( its process ( 4242 ) is gone )  ( --fix: the lock goes )
  ✗ /Users/me/proj → ~/proj: the next push stops, 2 files there would be lost
      notes/theirs.txt: not written by errand ( 2026-11-25 09:12:40 )
      config.json: changed there since errand wrote it ( 2026-11-25 10:03:11 )
  · /data/proj: 12 runs there, not here ( removed here, or never brought back )  ( --bring: brought back to … )
```

It looks at the copies ( removed by hand, without a manifest that says whose its files are, locked by a push that died, cut ), at what
the next push of each project would do -- the whole copy is walked, and nothing is done --, at the runs that are in one place
only, and at what is said to be there and is not ( where a run ran, a view's copy ). It only reads.

- `--fix` puts right what loses nothing: the record of a copy that is gone, a lock nobody holds, a run that is said to be there
  as well. **It never removes a file errand did not write, nor a run.**
- `--bring` brings back the runs that are there only ( those `errand attach` is to bring are not among them ).
- Without a name, every machine work was sent to within a day; `--all`, every machine; `here`, this one. `--json` says it as one document.
  It ends with 1 when something is wrong.

In [`errand web`](./web), each machine has a `check` button, which says the same, with a button for each of the two.

A copy removed by hand ( `rm -rf` ) is no harm: the next run makes it again. What is lost is what was in it only: runs
left by `--batch` and not brought back yet, what a run built there.

## Zones

What a run writes into the copy, outside `errand-outputs`, is declared once as a **zone**:

```python
errand.configure( zones = { "/results": "back", "build-*": "build", "/data-cache": "cache" } )
```

| zone | over there | after each command |
|---|---|---|
| `back` | written by the run, never pushed into | brought back here ( anchored path only ) |
| `build` | built there, never pushed into, never deleted | stays there |
| `cache` | added to by both sides, nothing replaced | what this side lacks comes back ( anchored path only ) |

A name ( `results` ) matches anywhere in the tree; a leading `/` anchors it at the root. A runner
whose tool writes somewhere of its own says so the same way:
[What a tool writes elsewhere](/guide/runners/#what-a-tool-writes-elsewhere).

### A cache zone

Downloaded data and results too costly to compute again belong to neither side. A **cache** zone
is added to on both sides, and nothing in it is ever replaced or deleted:

- the push sends what the copy lacks: a node with no internet finds the data downloaded here;
- what a run downloads or writes there stays, and is no change outside the zones;
- after each command, what the copy has and this side lacks comes back ( a path from the root only,
  as for `back` ), so the next machine gets it too;
- a file with other contents on each side is a **conflict**: said at each sync, left as it is on
  both. Name files after what they hold ( a hash, a version, the parameters ) and there is none;
- a deletion is never carried: a file removed on one side comes back from the other. Remove it on
  both. errand never prunes a cache zone.

Every project has one already, `errand-cache`, where [`errand.download`](./caching#a-file-at-a-url)
and [`@errand.memo`](./caching#a-result-computed-once) keep what they hold.

## What comes back

- **The run's record**, when it has written its `result.json`, which it writes last: a run still
  under way stays over there rather than coming back half written. → [Results and records](./output)
- **`--pull DIR`** and anchored `back` zones, after each command. → [Bringing back more](./output#bringing-back-more-pull)
- **Views**, as soon as a run names them: one file at once, or what changed in a directory.
  → [view](./steps#view-a-file-that-evolves)
- **Cache zones**: what this side lacks.

### Results are pulled, not pushed

A finished remote run stays over there until something collects it. A command you wait for collects
its results as its last step. [`errand attach`](./detached#close-the-laptop) collects those of a
command whose connection was lost, and `errand status` or `errand watch` those of a
[`--batch`](./detached) submission.

A pull never deletes here: a run you have that is gone over there stays. Nor does a push delete over
there the records of runs made there: a run tree a `--out` pattern put in the project ( `runs/…` ) is
left alone by the push for every run it holds that is not here, and the push says so.

## Next

[Data and results, computed once](./caching) · [Results and records](./output) · [Ssh](./layers/ssh)
