---
status: proposed
date: 2026-11-27
amends: 0013, 0035
---

# errand brings the files: tools resolve and install offline, from indexes and packages errand fetched

ADR 0013 gave a run `errand.download`, whose file goes up the chain of agents when the place has no internet. ADR 0035 made the
internet something asked for by **a request that waits, never through a link kept open** between a node and its login node
( Hugo: "un lien vivant entre le nœud et la frontale, c'est beaucoup trop coûteux" ), and left open `pip install` on a node with no
way out: Hugo wanted it done "sur la machine qui le demande ( … ) quitte à y faire plein d'étapes".

ADR 0044 makes every environment errand builds a thing of its own, resolved and installed where it is used, and identified by a lock.
Asked how such an environment is built on a machine without internet, Hugo, 2026-11-27: "Je voudrais qu'on arrive à utiliser errand
comme un proxy pour les download ( … ) que errand se charge de tous les downloads ( et donc les mette bien dans le cache, en "non
précieux" ), avec transmission à un agent parent s'il n'y a pas internet."

A first version of this ADR did it with an HTTP proxy served by the place's agent on `127.0.0.1`, to which the tools were pointed by
rewriting their URLs. Hugo, the same day: "je pense que ce n'est pas raisonnable de faire passer en clair des infos dans les canaux
errand". What was wrong with it: **TLS no longer went from end to end** ( micromamba and uv spoke HTTP to errand, which spoke HTTPS for
them: the tools no longer checked the origin themselves ), and **HTTP in clear on `127.0.0.1`**, where every user of a shared login node
can connect to a port. Neither the contents going through Hugo's machines, nor what they say of what is fetched, was the concern.

A design grill settled the points below ( `grill/downloads-proxy/` of 2026-11-27 ).

## Decision

**Trust comes from the lock, not from the way a file came.** Every file is checked against the sha256 the lock gives, where it is
installed, whatever way it took. A relay that was tampered with can make an installation fail; it cannot put another file in its place.
So the lock carries **sha256**, never md5, on which a substitution can be made on purpose ( ADR 0044's conda part ).

**A tool never touches the network**, to resolve or to install, whether the machine has a way out or not. One way, everywhere:

- **errand fetches**, itself, over HTTPS that it checks up to the origin ( conda-forge, PyPI ), on the first machine of the chain that has
  a way out. TLS goes from end to end between that machine and the origin.
- **Without a way out**, a request that waits ( ADR 0035 ) goes up the chain of agents, and the file comes down by the agents' own
  transport of files ( ADR 0006 ): the stdio of ssh between machines, a unix socket on a machine. **Nothing travels in HTTP, nor in
  clear, anywhere**, not even on `127.0.0.1`.
- Each file is checked by its hash when it arrives, kept in the cache, and laid out as a **local channel** ( `file://` ) for micromamba and
  a **local index** ( `file:///…/simple` ) for uv.
- micromamba and uv **resolve and install offline** on them, and check every hash again themselves: `micromamba create --offline -f`
  with an explicit list ( `url#sha256:…` ), `uv pip install --offline --no-index --require-hashes`.

The chain of trust is: an index, fetched by errand over TLS → the sha256 it gives → the lock → each file. The question of the tools' TLS
goes away: they speak to nobody.

**The solver runs on the machine that asks**, on indexes errand brought there, as Hugo chose: "moins risqué et va plus dans le sens des
caches bien gérés". Resolving elsewhere for its virtual packages ( `--platform`, `CONDA_OVERRIDE_*` ) was set aside.

**Conda: a reduced channel, from the sharded indexes.** conda-forge publishes, besides its whole index ( `repodata.json.zst`: 61 MB
compressed for `linux-64`, 30 MB for `noarch`, which micromamba fetches whole when it resolves online ), an index **sharded by package**:
a summary of 580 KB ( `repodata_shards.msgpack.zst` ), then one small file per package name, named by its sha256. micromamba 2.10 does
not read those; it reads any local channel that has a `repodata.json`. So errand reads the summary, fetches the shard of each package
asked for, then of its dependencies, from one to the next ( every version, which is a little more than needed ), and writes a **reduced
channel** that holds only those packages, on which micromamba resolves. A few MB where micromamba online loads 90, and all of it named by
its hash. The summary is checked again after 1 hour; a shard, named by its contents, never. A channel that publishes no shards is brought
whole, checked again after 1 hour. With no way out anywhere up the chain, the last summary known serves, and the resolution says so. The
resolved list is written into the lock with the origin's URLs, not the local channel's.

**PyPI: a loop on what uv says is missing, one step ahead.** uv resolves offline on a `file://` index ( `uv pip compile --offline
--generate-hashes` ), but there it opens the wheels themselves for their metadata: a `.metadata` file ( PEP 658 ) is not enough. When
something lacks, it fails and names it ( `requests was not found`, `failed to open file …/six-1.16.0-py2.py3-none-any.whl` ). errand runs the
resolution, fetches what was named ( the project's page, or the wheel, checked by the sha256 the page gives ), and runs it again. So as not
to pay one round trip per dependency, it fetches **beforehand** the pages of the declared dependencies ( `Requires-Dist` ) of every wheel
it brings. The wheels brought are mostly the ones that will be installed: nothing is lost. errand reads uv's messages, so **uv's version is
pinned**, as micromamba's is ( ADR 0044 ); a message it does not recognise stops everything and shows it.

**What is fetched is kept per machine, and is not precious.** `<cache>/downloads/…`, beside the store of environments ( ADR 0044 ), in the
format of errand's caches ( ADR 0020 ), so seen and removed by `errand caches` ( ADR 0022 ). A file nobody used for **14 days** goes by
itself, as an intermediate does ( ADR 0039 ): it can be fetched again.

**A relay keeps a copy.** A request from Jean Zay goes up ( its login node, then the laptop ) to the first agent with a way out; each agent
the file goes through keeps it in its own cache, not precious either: the next machine that asks finds it nearer.

**Whether a place has a way out is found, and may be said.** A short attempt, remembered a few minutes; `Ssh( internet = False )` says it
beforehand, and nothing is tried.

**A private channel** that needs a token is a field of the layer the day one is needed: errand is the one that fetches, so the one that
authenticates. None is in use.

**Images stay with `build_on`.** An image is built where there is a way out and carried ( ADR 0044 ); a `.sif` is one file, checked by
the sha256 its builder records, and by `apptainer verify` when it is signed. A recipe that starts from a local image ( `Bootstrap:
localimage`, `docker-archive`, `oci-archive` ) builds without a registry, should one ever be needed. Nothing here is for images.

**`errand.download` keeps its zone.** ADR 0013's downloads are a project's input data, in its synchronised `/errand-cache` zone; this
cache holds what tools need, per machine. Only the way up the chain is shared.

**Packages already unpacked keep micromamba's default.** A package unpacked in `pkgs_dirs`, then changed ( by hand, or by a process
that writes beside itself ), is linked as it is into a new environment, with a warning ( `safety_checks: warn` ); a change of the same
size goes without a word, unless `--safety-checks enabled --extra-safety-checks`, which hashes every file at every build. Hugo kept the
default: the hole is known, and written here. The archives themselves are always checked.

## Considered options

- **An HTTP proxy on `127.0.0.1`, the tools pointed at it by rewriting their URLs** ( this ADR's first version ): TLS broken from end to
  end, and a port every user of a shared machine reaches.
- **A `CONNECT` proxy, or `ssh -R` to the machine that has a way out**: TLS stays from end to end, but it is the live link ADR 0035
  refused, nothing can be kept on the way, and an `ssh -R` to a unix socket is no help: libcurl ( micromamba ) takes an HTTP proxy only
  over TCP.
- **A proxy that decrypts**, with a certificate authority errand makes and hands to every tool: a TLS man in the middle on the user's
  machine, and each tool reads its certificates its own way.
- **Resolving where there is a way out, with the virtual packages of the machine that asks**: possible ( `--platform`,
  `CONDA_OVERRIDE_CUDA`, `uv pip compile --python-platform` ), but the solver does not run where the environment will live, and the indexes
  are not kept where they serve.
- **Not resolving without a way out**: a machine must find a resolution of the lock it can take, or stop. Pushes the work onto the person.
- **Whole indexes for conda**: what micromamba does online, 90 MB for conda-forge on one platform, carried up and kept at every relay.
- **Wheels made of metadata alone**, built by errand from PyPI's `.metadata`, for uv to resolve on without a loop: a trick of errand's
  own, and every version of every dependency to walk through.
- **The tools fetching for themselves where there is a way out**: two ways to test, two caches, and the relays get nothing from it.

## Consequences

ADR 0035's open question, `pip install` where there is no way out, is closed: the tool runs on the machine that needs it, and what it needs
comes as files through requests that wait. The agent of a login node whose nodes have no way out stays while they have requests, as ADR
0035 already assumes. errand now reads two formats it did not ( conda's sharded index, in msgpack; PyPI's simple pages ), and uv's messages.
The first implementation serves the environments of ADR 0044 on a machine that has a way out, through the same path; the relay comes next.
