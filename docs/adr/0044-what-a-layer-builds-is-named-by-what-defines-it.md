---
status: proposed
date: 2026-11-27
amends: 0004
---

# What a layer builds is named by what defines it, kept in errand's caches, and never changed once built

Hugo, 2026-11-27, reading the `Micromamba` layer: "Si errand gère les ressources et a pour objectif de rendre les calculs répétables, il
faudrait que le nom et l'endroit de l'env Micromamba soit géré par errand dans les caches. Du coup, il faudrait idéalement se baser sur
un hash du contenu demandé par les paquets conda et pip." And of the other layers: "Apptainer devrait en principe se baser sur un .def, et
sauf mention contraire le .sif est dans le cache."

What was there. A `Micromamba` environment was known by the `name` the user chose, in the user's own root prefix ( `~/.mamba/envs` ), and
brought up to date **in place**: `install` into it when it existed, `create` when not ( `layer/build.go` ). So:

- a package taken out of the declaration stayed installed, under a fingerprint that said the environment was current;
- what an environment held depended on the order of its updates, not on its declaration;
- a failed update left it half done ( ADR 0004 said so );
- a change by hand was adopted again, without a word;
- two projects that declared the same `name` wrote into one environment.

An Apptainer image was a path of the project, and only its `.def` was hashed: a file its `%files` copies could change without a rebuild,
as could a Docker build context. `Uv` and `Venv( create = True )` lived in the project, updated in place.

A design grill settled the points below ( `grill/content-addressed-envs/` of 2026-11-27 ).

## Decision

**One rule for every layer that builds.** What it builds is named by what defines it, kept in errand's caches, and **never changed once
built**. A changed declaration makes **another** artefact; the one before becomes a cache entry, which goes once nothing uses it. It applies
to `Micromamba` and `Conda`, `Uv`, the images of `Apptainer`, `Docker` and `Podman` built from a `recipe`, and the disk of `Windows`. Not to
`Nix` and `Guix`, whose stores already are this; nor to `Module`, `Vars`, `Ssh`, `Slurm` and the others, which build nothing. `Micromamba`
is done first.

**A layer that only names something names the user's.** `Micromamba( name = "x" )` alone, `Conda( name = "x" )`, `Apptainer( image = … )`
without a `recipe`, `Docker( image = … )` without one, `Venv( python = … )`: errand uses them, never builds, updates nor judges them, and
keeps no record of them. A layer that declares its contents ( `packages`, `python`, `pip`, `requirements`, `develop`; a `recipe` ) is
errand's. ADR 0004's **adoption** goes for these layers: what is errand's is always built by errand, and what is not is never looked at.
`Venv` loses `create`, `requirements` and `pip`: a venv of errand's is a `Uv` one.

### An environment is identified by its lock

**The lock is the identity; the declaration says when to resolve.** A declaration with `numpy` unpinned gives another environment next
month, or on another machine. So the resolution is written down, in **`errand-envs.lock`**, one file at the project's root beside
`errand-envs.py`, JSON indented and sorted ( which yaml reads too: errand's program has no yaml library ), committed with the sources ( as pixi's lock or `uv.lock` ). Per environment and per resolution ( below ), it holds
the hash of the declaration it was resolved from, the conda part as an explicit list ( URL and sha256 of each package, never an md5: ADR 0043 ), and the pip part
( exact versions, with the hashes of **every** wheel of each version ). An environment's identity is the hash of its resolution. A
declaration whose hash differs from the lock's is resolved again; `errand setup --update` resolves everything again on purpose. The pip part
goes through **uv** ( `--no-deps`, `--require-hashes` ), which errand already fetches.

**Resolved on the machine that builds it**, with its own virtual packages: the solver sees nothing else of a machine ( `__unix` / `__win`,
`__linux`, `__osx`, `__glibc`, `__cuda`, `__archspec` ). lmo and Jean Zay are both `linux-64`, with neither the same driver nor the same
glibc. Hugo wanted the lock to say "tout ce qui a de l'influence". A dry run ( `--dry-run --json` ) gives each chosen package's `depends`
and `constrains`, so what the solution requires of the machine ( `numpy` → `__osx >=11.0`, seen here ); it does not show what an
**absence** decided: on a machine without a card the solver takes jax or torch for the CPU, and no package of the solution names
`__cuda`. So each resolution in the lock keeps:

1. what existed when it was made: the platform, and **whether `__cuda` was there** ( the one virtual package whose absence changes the
   builds chosen; the others act through `>=` constraints );
2. the **constraints** its packages put on virtual packages, read from the solution ( `__glibc >=2.28`, `__cuda >=12`, `__archspec` for
   builds per micro-architecture level ).

A machine takes a resolution of the lock when it has the same presence and meets all its constraints; otherwise it resolves, and adds its
own. lmo ( CUDA 12.4 ) and Jean Zay ( 12.2 ) share one as long as no package asks for `__cuda >=12.4`. `cuda = False` in a declaration
takes the CPU builds on a machine with a card. On the pip side, a machine takes the wheel of each version that suits it ( `manylinux_2_28`
by its glibc ), and resolves on its own when none does. A card's compute capability, an MPI or drivers of the system, CPU features beyond
`__archspec` are not inputs of the solver: the record notes them ( `nvidia-smi`, `lscpu` ), to understand a failure, and they name nothing.

### Where it is kept

**A store per machine**, shared by the projects: `<cache>/envs/…`, in the `cache` of errand on that machine ( `Ssh( cache = … )`, `$WORK`
at Jean Zay ). Two projects with the same resolution share one environment. The packages micromamba unpacks ( the `pkgs/` of errand's root prefix, `<cache>/envs/root` ) are in the
store, **on the same file system**, so that an environment is hard links and not a copy. An environment is built under a lock, and is
complete when its last line is written: a prefix without it is removed and built again. A conda prefix cannot be moved, so nothing is built
aside and renamed.

**errand's own micromamba**, fetched and pinned in `<cache>/micromamba/<version>`, as uv is: nothing to declare on a compute node, and the
user's root prefix is never read. `executable` stays for a site that imposes its own. An environment of errand's is always built with it,
whether the layer says `Micromamba` or `Conda`; `Conda` is otherwise for environments of the user's.

**What goes.** An environment that a project registered on the machine declares ( its lock ) never goes by itself. One that no project
declares any more goes after **30 days** unused ( 7 on a shared machine, ADR 0028 ), so that a branch switched away and back finds it.
`errand caches` shows it and removes it by hand ( ADR 0022 ); the packages no remaining environment links go with it.

### The packages one works on

**`develop = [ "../sdot" ]` installs into an overlay, never into the environment.** Its code changes all the time, so it cannot be part of an
identity, and installed into an environment shared by projects it would be seen by all of them. Per project and environment, a venv
`--system-site-packages` over the environment, in `<outputs>/caches/build/<env>/develop` ( ADR 0039 ), where errand installs each path
`-e --no-deps`. It is thrown away and made again in seconds.

**Their dependencies go into the environment.** Hugo: "Est-ce qu'on peut se débrouiller pour que les dépendances de sdot soient bien dans
le micromamba et pas dans le .venv ?" errand reads `[project].dependencies` of each path ( with the extras asked, `../sdot[viz]` ) and
`[build-system].requires` ( an editable meson-python or scikit-build package builds again at import, with the environment's tools ), and adds
them to the environment's declaration before resolving: they are in the lock. `dynamic` dependencies are asked of the build backend
( `pip install --dry-run --report` ). The packages of `develop` are taken out of each other's dependencies: no `loom` from PyPI under the
editable `loom`. The conda part is resolved first, then these go in through uv, which leaves what conda already holds. A dependency wanted
from conda-forge rather than PyPI is declared in `packages`. A change to `sdot`'s dependencies changes the declaration, so a new environment;
a change to its code changes nothing.

### By hand

**`name` is a label**, what `errand envs` shows. errand writes nothing into the user's root prefix: a link `~/.mamba/envs/<name>` would let
`micromamba env remove -n name` empty the shared environment through it. By hand, `errand shell --env X`, and **`errand python --env X`**,
which prints the interpreter to use ( the overlay's when there is one ), for an editor or a `$( errand python )`.

**An environment changed by hand stops the run.** errand compares it with its lock ( `conda-meta/*.json`, the pip list ), refuses to start,
says what changed ( added, taken out, another version, on either side ), and proposes the declaration's lines that would make that the
intended state ( `packages = [ …, "scipy" ]` ). It does not edit `errand-envs.py`: it is Python. Changed, the declaration gives a new
environment, built beside. `errand setup --force` builds it again from the lock, in a new directory ( `<hash>-2` ), and the changed one goes
with the cache's tidying. `--no-setup` runs in it anyway, as it runs in an image whose build failed.

### Images and the rest

**An image cannot be locked** ( `apt-get install` in a `.def`, `From: ubuntu:24.04` that moves ), so its identity is the hash of its
**whole** declaration: the `.def` and the files its `%files` copies, `build_flags`, `pip`; the `Dockerfile` and the context it copies ( what
`.dockerignore` leaves ). `image` is optional: by default `<cache>/images/<hash>.sif`, or the tag `errand/<project>:<hash>`; an `image` given
with a `recipe` is a name for the last one built. The record keeps what came ( the digest of the base, `pip freeze` in the image ): known,
if not made again. `install` and `run_from` ( IDRIS ) do not change, and `build_on` is where the same key is looked for.

**`Uv` takes `uv.lock` as its identity** when the project has one ( `uv sync --frozen` into `<store>/uv/<hash>` ); otherwise it is treated as
`Micromamba`, without the conda part.

**The disk of `Windows`**: a base image shared by projects, named by the hash of what defines it ( the ISO's hash, `index`, `msvc` ), and a
qcow2 overlay per project over it. Windows and the Build Tools, the long part, are installed once per machine; an overlay that fails is
thrown away without touching the base.

**A machine without internet** builds its environments through errand's downloads ( ADR 0043 ): resolved and installed there, the files
asked for up the chain of agents. No `build_on` for environments.

## Considered options

- **The hash of the declaration alone.** Simple, but the same declaration is not the same packages: lmo and Jean Zay drift apart, and an
  environment built again after a tidying is no longer the one that ran.
- **The hash of the declaration, the lock only kept in the record.** What came is known, and cannot be made again elsewhere.
- **A lock keyed by platform only.** Two `linux-64` machines with different drivers would share one that does not run on one of them.
- **Resolving here for every platform, as pixi does**, with the virtual packages of each machine declared: one place resolves, but what the
  machines have must be written down and kept true, and pip resolves badly for another platform.
- **A store per project** ( `<outputs>/caches/envs` ): tidied with the project, but nothing shared, and the packages hard linked only when
  the outputs are on the same file system as `pkgs_dirs`.
- **Reusing an environment by moving it** ( `mv` when packages are only added ): a conda prefix has its path written into shebangs and
  files at installation, and cannot be moved. What a move would save, the shared packages already save: a new environment is a resolution
  and hard links.
- **Editable packages installed into the environment, outside its hash**: the environment is no longer what its name says, and another
  project sharing it sees this one's `sdot`. **On `PYTHONPATH`**, with nothing installed: a package with a build step does not work.
- **The environment made read-only** against changes by hand: its files are hard links of the package cache, which would be read-only too,
  and packages that write beside themselves at run time ( numba's caches, `__pycache__` ) break or slow down.
- **A link `~/.mamba/envs/<name>`** to keep `micromamba activate name`: the overlay would be missing anyway, and a removal through the link
  empties the shared environment.

## Consequences

- The environments in the user's root prefix ( `~/.mamba/envs/prj-jax`, `loom-jax`… ) are not taken over: nothing is kept compatible, as
  for the port to Go ( ADR 0034 ). A declaration with packages gets an environment of errand's at its first command, and its first lock;
  the old one stays the user's, and `errand envs` says once that an environment of that name exists there, unused.
- `root_prefix` only finds an environment of the user's; ADR 0004's records move with the artefacts into the store ( `envs/mamba/<id>.json`,
  and `envs/refs/` for what each layer of a project runs in ); `Kept` no longer protects `.venv` or images in the
  project, which are no longer there.
- Hugo's global instructions ( "a micromamba environment named after the project", `micromamba run -n` ) and the errand skill are rewritten
  to go through `errand run`, `errand shell` and `errand python`.
- `errand-envs.lock` is a file errand writes into the sources, to be committed. Only the side that drives a command writes it, from
  what the agent that resolved says: changed over there, it would stop the next push ( ADR 0038 ). A machine whose resolution did not
  reach the lock ( a run nobody followed ) says it again at the next command. Two machines resolving the same environment at once may
  conflict in git; the lock is indented and sorted, to keep the conflicts readable.

## As built ( 2026-11-27 )

- **Micromamba, Conda, Uv** as decided. The store is `<errand's directory>/envs/{mamba,uv}/<id>`, a prefix complete once its
  `errand-built.json` is there; `envs/refs/` says what each layer of a project runs in. `errand python [--env X]` is the
  command named above. The lock is written by the side that drives, from a note of the agent ( `lock` ).
- **Uv** has no `path` any more: no `.venv` in the project. With a `uv.lock` and no packages of its own, `uv sync --frozen
  --no-install-project` into the store, the project itself `-e` in the overlay.
- **Venv** names an interpreter, and nothing else.
- **An image's** identity is its whole declaration, the files `%files`, `COPY` and `ADD` take included; one errand has no
  record of is built again, aside. An Apptainer image with no `image` is `<errand's directory>/images/<recipe>-<hash>.sif`:
  one there was made by errand, for whichever project. `build_on` builds it in the builder's cache and carries it into the
  cache where it runs; the agents say where each is. A Docker image still names its tag.
- **Windows**: the installed image goes to `<errand's directory>/windows/<name>`, shared; the project keeps its data disk. It
  was already named by what it is installed from; an ISO of the project now names it with the project's path. An image the
  project installed before is still booted where it is.
- `errand caches` shows the store of each machine ( environments, images, installed Windows, and what no project declares ),
  and tidies it as the agent's round does; `--unused DAYS` and `--dry-run` as for the libraries' caches.
- Not done: dynamic dependencies of `develop`.
