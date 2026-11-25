---
status: accepted
date: 2026-11-25
---

# Builds go where errand keeps its caches; what is costly stays longer than what is not

Hugo, 2026-11-25: now that errand manages caches, every build should be made in directories errand manages: loom's
first ( its intermediate files, and its `.so`, worth more than its `.o` ), then whatever a project builds -- "for cmake
for instance, I would like errand to provide, per project, a cache directory one can find again, to put all the mess in".

What was there: `{build_dir}` was `build/<env>` in the project, given to a run only as a string, listed nowhere and never tidied.
loom already wrote under `$ERRAND_CACHES/loom`, but declared no intermediate, and errand removed none ( ADR 0020, rule 3: "in the
format, not removed yet" ). And loom collected a catalogue of kernels into the package's sources ( `src/sdot/kernel-catalogue` ), where an
import of the checkout then found it.

## Decision

**A directory per project, place, environment and name.** `<outputs>/caches/build/<env>/<name>`, beside the libraries' caches: on the
disk the outputs are on ( a scratch disk, when one is named ), never part of a sync ( `errand-outputs`, and tagged `CACHEDIR.TAG` ).

- Every run is handed `ERRAND_BUILD_DIR` ( the directory named `main` ) and `ERRAND_BUILDS` ( what holds the names ), runner or not.
- `errand.build_dir( "name" )` in Python; `errand build-dir [--env ENV] [NAME]` prints it, made: `cmake -B "$( errand build-dir )"`. By hand it
  is the same directory as under errand: that is what makes it found again.
- `{build_dir}` of a runner is that one. `configure( build = … )` still says otherwise, and is then the project's to tidy.

**A build directory is a cache entry** of ADR 0020's format, in a cache called `build` whose writer is errand: so the storage tab, `errand
caches`, the removals by hand show and do for it what they do for a library's. A directory somebody made there without saying is found, and
dated by what is in it.

**Two levels** ( ADR 0020's rule 3, now applied ). What can go alone goes first: an entry not used for **14 days** loses its
`intermediate` paths and keeps its `product`. For a build directory, which says nothing of what it holds, the intermediates are what every
tool makes again: compiled objects ( `.o`, `.obj`, told by how they begin: an `.obj` may be a mesh ), `.gch`, `.pch`, cargo's `incremental/`. The entry itself goes as before: by its dependencies,
or by hand ( `errand caches --unused DAYS` ). Nothing precious goes by age alone.

**loom** declares a kernel's own objects as its intermediates, and serves a kernel whose objects are gone as long as all else is as ninja
left it. The objects of the units and of the runtime are not declared: made again, they would be newer than every kernel
that links them, which would all be made again.

**A catalogue is never in the sources.** loom collects it in its cache ( `<loom's cache>/catalogues/<package>` ), puts it in the **wheel**, and
nowhere else: `<package>/kernel-catalogue/` exists in an installation only. A checkout does not serve one by itself -- its headers move,
and it would serve stale binaries; `LOOM_CATALOGUE_DIR` ( or the package's own variable ) names one explicitly. What a package says is
`register_catalogue( <package>/kernel-catalogue )`, as before: the directory is not there in a checkout.

**Not launched by errand, all of it works.** Hugo, the same day: it is very important that loom, sdot and the like run when they are not
launched by errand; the fallbacks must be there, clean and reliable. What is always there is errand's Python ( `errand-lib`, pure Python, a firm
dependency of these libraries ); what may lack is a run, and errand's program. So the fallbacks are in errand-lib only, once: `library_cache` gives a
library's cache ( a run's, else the user's, else a temporary directory of the user's alone, refused when another may write in it ), `download`
fetches by itself, to the place and under the locks the program uses, `cache_dir` and `build_dir` answer outside a project, a view is said on the
error output. A cache that was asked for and cannot be written is said. loom's is tidied by `python -m loom.compilation.cli prune [--objects]`, by what
was used. What needs the program ( `dispatch_wheels` ) says so. `errand-lib` must then be published before the libraries that need it. A run is handed the Python of the program
that launched it, which may be older than the `errand-lib` a library asks for: loom bears it for now ( `_alone`, `$ERRAND_CACHES` alone ), until every
errand in use has `library_cache`.

**What declares environments says what it read** ( a provider answers the paths of its files ): the document errand keeps of a project was not
made again when `loom-builders.py` changed. `$ERRAND_PROVIDERS` names providers of a package that is on the path without being installed.

## Considered options

- **The library, or the project, chooses** ( `build/` ): seen by nobody, synced by mistake, a `.so` in the sources imported instead of the wheel's.
- **A directory per user, not per project**: two checkouts with the same name of target in one CMake cache.
- **One directory for every environment**: a CMake cache of one toolchain read by another.
- **Removing whole build directories by age**: an hour of compilation for a few hundred MB. By hand, seen first.
- **The catalogue copied into the sources for the time of `pip wheel`**: an interrupted build leaves it there.

## Consequences

The first build after this is a whole one: `build/` of the projects is no more used, and is theirs to remove. `.gitignore` lines for
`kernel-catalogue/` go with it. A wheel is made of what is in the cache, added to it after `pip wheel`: the sources are not touched.
