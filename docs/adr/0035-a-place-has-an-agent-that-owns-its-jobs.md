---
status: proposed
date: 2026-11-24
---

# A place has an agent, which owns its jobs; five programs

ADR 0034 made errand in Go a new program. Its first piece that reached another machine ( 2026-11-24 ) went against
what errand is built on, without saying so: no agent on the place ( a process as long as one command ), the run
"started by the errand over there, as one typed there", ssh's own `ControlMaster` in place of what keeps a link, and
"the side that typed has internet". Hugo, the same day: "Remettre en cause des conceptions est très sain, mais il faut
prendre la peine de comprendre pourquoi elles étaient là avant de les virer."

So this ADR says the foundations again, as read in ADR 0002, 0005, 0008, 0009, 0010, 0013, 0014, 0016, 0022, 0024,
0025, 0028 and 0029, and what was decided on top of them with Hugo on 2026-11-24. A page with drawings went with the
discussion; this is its record.

## What stands

- **Agents exist because crossing the stack costs, each time** ( ADR 0005 ): an ssh, a container, a VM's boot. Long
  lived processes sit where the stack leads, and one side drives them. There is no second errand deciding over there.
- **One protocol at every hop** ( ADR 0005 ): each layer that leads elsewhere gives a byte stream ( the stdio of `ssh`,
  of `srun`, of `apptainer exec` ); the same frames cross it; an agent relays to those further without reading.
- **An agent is not at the end of the ssh pipe**: it stays over there, listening on a socket; ssh only starts a bridge
  to it. A connection lost, a laptop closed, lose nothing.
- **What runs on a machine is owned on that machine** ( ADR 0008 ), written on disk; a stop's escalation is the
  owner's. An agent killed outright loses its jobs: the next one kills them, it does not adopt them. Hugo, 2026-11-24:
  "En principe, on ne tue pas un agent, il s'éteint de lui-même. Si jamais on le tue, c'est normal qu'il perde ses
  jobs."
- **Agents execute; the side that drives decides** ( ADR 0005 ), with what only the place knows resolved on the place
  ( ADR 0002, 0011 ).
- **A place in a machine's queue is taken where the work runs**, never by a process tied to a connection ( ADR 0005,
  0014 ).
- **Internet is taken from the nearest agent that has it** ( ADR 0013 ): a request goes up the chain and stops at the
  first agent that can answer. The side that typed is the last resort, so that links are not filled for nothing.
- **Agents say what changes** ( ADR 0024 ); nothing is asked again and again.
- **Sharing is decided per directory, never per machine** ( ADR 0022, 0028 ): a directory of errand's has an id; two
  machines that read the same id share it.

## Decided, 2026-11-24

### Five programs

> **Changed the same day: four, and no orchestrator apart** -- see "No orchestrator apart" below. The table is what
> was first decided.

| program | what it does | how long it lives |
|---|---|---|
| **cli** | reads what is typed, hands it to the orchestrator, draws the terminal from what it is sent ( ADR 0016 ). Very basic | a command |
| **web** | the page; subscribed to the orchestrator's events instead of asking | while it is kept open; on few machines |
| **tui** | as web, in a terminal. Last | while open |
| **orchestrator** | reads the project, chooses environments and the grid, decides what to install ( requests may be grouped ), pushes and brings back files, keeps the record of the runs started from here | one per point of entry ( a laptop, a desktop ): a daemon, started by cli or web when it is not there, gone after its `keep_alive` |
| **place agent** | everything a place offers ( below ). One per place, this machine included | while it has jobs under it, then its `keep_alive`. May become a relay |

Several orchestrators may reach one place ( a job launched from the laptop, another from the desktop ), and several
versions of them: what must be serialized between them -- "I am going to install this in that environment" -- goes
through the place's agent, as **locks and intents**. An orchestrator older than the agent it finds is told to update.

A run started from one point of entry is seen from another by the place's table; its directory comes back to the one
that started it, and a list of where the directory is present is kept everywhere ( as ADR 0030 does for copies ), so
that it can be loaded and looked after from elsewhere.

### What a place's agent offers

Well beyond starting what it is told: its **jobs** ( start, follow from an offset in their files, input, stop );
**files** ( the project's copy by manifest, what comes back ); **installations**; **what its runs ask** ( an
`errand.download` with no internet, a `step`, a `view` ); **what changes**, said to whoever subscribed; **what is kept
warm** ( a VM, an allocation, a container entered ); **the relay** to the agents further.

### An agent where what one sees changes

Another machine, a VM, a node of an allocation: the link can break apart from the processes ( ADR 0008's own
criterion ). The inside of a container too: its files and its tools are not the machine's, and an agent does more than
start commands -- it probes what the place has, builds the layers that follow, resolves targets, looks after caches as
seen from there. It is the same program, started once through `apptainer exec`.

No agent for a layer that only sets the command's surroundings ( `Vars`, `Module`, `Venv`, `Micromamba`, `Conda` ):
the machine and its files are the same. The variables are computed once, kept, and the machine's agent starts the job
with them. Measured on lmo: 36 ms to compute a micromamba environment's ( 370 bytes ); `micromamba run … true` itself
115 ms; `apptainer exec … true` 300 ms each time.

So: one hop in general, two at most ( a VM behind ssh, a node behind its login node ).

To be tried before it is relied on: with apptainer an image is unmounted 1.5 s after `exec`'s own process ends
( ADR 0029, step 6 ) -- a container's agent must stay that process.

### The agent owns its jobs; an update is a relay

ADR 0029 gave every job a keeper of its own, so that an update of errand would not touch what runs. Measured in Go:
12 MB per process, 24 MB per job as written on 2026-11-23 ( keeper and session ), 1.5 GB for 64 jobs at once.
Hugo: "Je ne vois toujours pas l'impératif de process par job. L'agent reste relativement simple, il ne va pas
« tomber ». Par ailleurs, si on veut une mise à jour, on pourrait très bien imaginer le mettre dans un rôle de relais,
et coexister avec la nouvelle version le temps que ses jobs finissent."

- **A job is its place's agent's**: no process of errand's per job.
- **An update**: the agent of the new version takes what is asked from then on; the one before takes nothing new,
  answers for its jobs through the new one ( follow, input, stop, state ), and goes when its last job ends.
- What stays the same from one version to the next is then small: what a newer agent asks of an older one, and a job's
  record on disk.
- **Of ADR 0029 these stay**: `--batch` is the same path as a run that is waited for; a stop is said, silence is not
  one; output goes to files, the channel only streams it live. **The lease goes** ( a day for a job not kept that
  nobody follows ): a run nobody follows any more goes to its end, and `errand web` shows where it is.

### `--batch` detaches the command, and nothing else

On the place, a run that is waited for and one launched with `--batch` are the same job. Hugo: "la seule chose que
`--batch` doit changer, c'est que le processus est détaché sur le portable."

Under Slurm, one path where the Python had two ( an `srun` waited for from the login node, or an `sbatch` driven by a
file -- ADR 0005's "schedulers cut the chain" ):

- everything goes by `sbatch`, whose step is the node's agent;
- the login node's agent reaches it with `srun --jobid … --overlap` when somebody looks or types; that link may drop
  and be opened again, stopping nothing;
- an allocation kept warm is only that node agent's `keep_alive` ( none by default on a cluster ).

cinaps has what it takes ( Slurm 24.11.5, `--overlap` ). Not tried end to end.

### An installation does not depend on the laptop

Automatic installation asks nothing of the user ( the guide's "Automatic installations" ); closing the laptop must not
stop it. The orchestrator hands the agent a whole intent ( "bring this environment to this state, then start this
run" ), and the agent carries it out alone. So the agent knows **how** to build what a layer declares -- which is where
a built thing's record and the rule of adoption are anyway ( ADR 0004 ) -- and the orchestrator decides **what**,
**where** and **in which order**. The agent is less simple for it; the relay is what makes that acceptable.

A step that needs another machine ( built elsewhere then carried, a download only the laptop can make ) waits for
somebody who links the two.

### Machines and file systems

A process belongs to a machine; a file belongs to its directory.

- **The machine's**: its jobs and their record ( a pid means something there only ), its queue, its sockets. On a home
  that is shared, kept under the machine's name.
- **The directory's**: a copy and its manifest, what was built and its record, caches, the program that was sent ( one
  per system, not one per machine ), and **the locks and intents about them**. They are written in the directory, not
  in an agent's memory: a login node and a compute node see the same home, and so does an agent started later. A lock
  is a directory made by `mkdir`, atomic on NFS and Lustre ( ADR 0013 ). An intent says which machine does the work,
  since only that machine can say whether the process still lives.
- **One machine looks after a directory** for the daily round: the first that reached it, unless chosen ( ADR 0022 ).

### Requests that wait

What is interactive goes by the stream ( `ssh`, `srun` ): live output, what is typed, a terminal -- it means something
only while somebody is there. What must reach its end with nobody there goes by files: a download asked for, an
intent, its answer -- the same messages, a file each instead of a frame, in a box on the shared directory ( as ADR
0013 already does for a download's answer ).

- one box per login node's agent, however many nodes: it watches a directory, not a connection per job;
- a request left there is not lost when the agent stops: the next one finds it. Every parent agent may stop and
  connect again;
- the login node's agent stays while a job it submitted runs. On a cluster that kills long processes on its login
  node, requests wait in the box.

Limits, known: on NFS a file left by a node may take tens of seconds to be seen from the login node ( attribute
cache ) -- fine for a download, not for anything interactive; and with no directory shared between a node and its
login node, the stream is all there is. Hugo: "Je crains un peu pour la fiabilité du download sur gros cluster, mais je
propose qu'on voie ça au fil de l'eau."

## No orchestrator apart ( 2026-11-24 )

Hugo, once the orchestrator was a daemon: "Maintenant qu'on a décidé de mettre des relais pour gérer les versions
différentes et qu'en plus on s'est débrouillé pour minimiser le nombre d'agents, je pense que la séparation en
orchestrateur se justifie moins. Peut-être qu'on pourrait supprimer l'orchestrateur et tout mettre dans les agents qui
en pratique savent quasiment tout faire. [...] Par contre, il me paraît important que lancer errand run ne lance pas
le gros processus qui fait tout ce que fait un agent. Donc [...] un errand CLI, qui va chercher l'agent ( responsable
du lock ), qui lui est unique par ressource."

- **Four programs**: cli, web, tui, and the agent. A command typed on a machine is carried out by **that machine's
  agent** ( `commands.go` ): it chooses environments and the grid, reaches the agents of other machines, pushes,
  starts jobs there or here, follows them, brings their files back. It is the chain of ADR 0005 again: an agent
  reaches the agents further.
- **The cli is small, and starts nothing that does the work** ( `cli.go` ): it finds this machine's agent -- one
  process per machine and per version, which holds its jobs and its locks --, hands it what was typed with the
  directory and the surroundings it was typed in, and draws.
- **The cli reads the project**: the agent asks it for the declarations of a root when it needs them, and the cli
  runs `python -m errand.declared` in the shell the command is typed in. An agent starts no Python.
- **The links to other machines are the agent's**, kept between commands; a command's own way to its agent is closed
  with the command.
- **Nothing depends on a parent staying.** Hugo: "je serais plutôt pour éviter de dépendre des parents. L'idée, c'est
  quand même qu'un parent peut s'éteindre ou être indisponible et que le processus enfant puisse continuer sa vie. Le
  download est une exception et je dois dire qu'elle m'embête un petit peu." A job is its place's agent's; an
  installation is carried out by the agent that holds it; what comes back comes back when somebody is there to take
  it. The download of a place with no internet is the one thing that waits for somebody else.
- The question that held the programs' split back is gone with the orchestrator: a run that types `errand run` on a
  place finds an agent there that knows how to carry it out. What a place is sent is the agent and the cli.
- Gone: `orchestrator.go`, the daemon of its own, `ERRAND_ORCHESTRATOR`, the mode inside the command's process ( the
  agent is a daemon anyway: it holds the jobs ).

Tests: `e2e_test.go` whole, three times; "a command is carried out by the agent" -- one agent serving the commands
that follow, a command's own surroundings and directory, the project read with the Python of the shell that typed
the command, the second command to a machine logging in nowhere.

Measured from the Mac: a run on lmo in 0.11 s, a run here in 0.03 s; the command line's process 13 MB for the time
it lives ( it is still the whole program, of which it runs little ), this machine's agent 35 MB.

Not done: the cli and the agent as two files ( nothing stops it now ); a project's declarations sent with a run, for
a command typed inside that run on a place with no Python.

## Agents with a responsibility stay while agents depend on them ( 2026-11-24 )

Asked whether, under apptainer, resources are managed by an agent above the container and not by the one inside,
and offered "the claim held by the job's own process, so that it depends on no agent", Hugo: "L'agent du job peut
partir avec le job, par exemple si c'est lancé sur un apptainer. Par contre, un agent de ressources doit rester en
place. Je pense qu'on est contraint d'avoir le principe d'agents à responsabilité qui doivent rester tant que les
jobs qui en dépendent ne sont pas finis. Ou plus précisément, tant que les agents qui en dépendent ne sont pas finis.
Ça sera le cas, par exemple, des agents qui ont accès à Internet alors que ce n'est pas le cas des enfants, ou alors
des agents qui gèrent des ressources d'une machine."

- **An agent may have a responsibility**: the resources of a machine ( its queue ); the internet, where the agents
  further have none. Such an agent **stays while agents that depend on it live** -- past its keep, and when another
  version retired it.
- **An agent that depends on another says so** ( `depend` ), for as long as it lives. The one it depends on gone,
  whatever made it go, it stops what it holds and goes: nothing runs under an agent that is not there.
- **Resources are granted by the agent that holds them** ( `claim` ), and held there for the job: the agent of the job
  takes nothing itself. A container isolates neither cores, memory nor cards -- they are the machine's.
- The agent of a job may go with its job.

## Kept only while used; the internet by requests that wait ( 2026-11-24 )

Hugo, on the principle above: "les agents qui peuvent être responsables de ressources seront maintenus uniquement
lorsque des agents ou eux-mêmes ont des jobs *sur cette même ressource*. Si c'est strictement pour les ressources,
il n'y aura pas d'agent maintenu sur la frontale d'un cluster, vu que les ressources sont les nœuds de calcul et non
la frontale." And, offered the internet as a proxy kept open between a node and its login node: "un lien vivant
entre le nœud et la frontale, c'est beaucoup trop coûteux. Et surtout, on pourra avoir besoin de faire du download y
compris après l'installation. Je propose qu'on suppose pour l'instant que le processus sur la frontale ne sera pas
tué."

- **An agent that holds a resource stays while that resource is used**: while it holds a place in the machine's queue
  for somebody, or an agent under it has a run going. Not for the life of the agents under it: one of those with
  nothing going keeps nobody, and goes when the agent above does.
- **Nobody is elected**: a machine's queue is files under a lock, so an agent that sees that directory takes its
  places itself; only one that does not see it ( inside a container ) asks the agent that started it. Under a
  scheduler there is no queue of errand's, and so no agent kept for resources -- on the login node or on a node.
- **The internet is asked for by a request that waits, never through a link kept open.** A run with no way to a URL
  leaves a file in its project's downloads; whoever sees that directory and has a way answers. The agent of a login
  node, where its nodes have none, is **assumed to stay**: what happens where a robot kills it is left for when it
  is met ( a request left there is not lost: it waits ).
- `pip install` on a node with no way out is not covered by this: it needs a way out for the time it runs, not a
  file. Left open -- installing from the login node where the disk is shared, or fetching what `pip` needs as files
  first.

Looked at on cinaps, to know what the question is there: its compute node reaches pypi by itself ( nothing to
relay ); a node reaches its login node by ssh; nothing kills a user's processes at logout, no CPU limit, no `at`.

## `pip install` where there is no way out ( Hugo, 2026-11-24 )

"Je préfère que ça se fasse sur la machine qui le demande ( pas forcément la même arch, etc. ), quitte à y faire
plein d'étapes." So the tool runs on the machine that needs the packages, and what it needs comes as files asked for
through errand's requests. Not designed further yet. One way: `pip` reads an index from a directory
( `--index-url file://…/simple` ); run against one that holds only what was fetched so far, it says what is missing
-- a package's page, then a file --, which is asked for, and `pip` run again, until it says nothing is.

## One program, not four ( Hugo, 2026-11-24 )

"Five programs" is revised: the command line, the agent, the page and the screen are one program, whose first word says which it
is. Apart, they saved two milliseconds at each command ( 3.6 ms against 5.5 ) and cost a distribution two and a half times its size ( 51 MB
for five systems, compressed, against 21 ): with one, a distribution carries every system.

**A daemon runs from a copy of its own**, `<cache>/bin/<hash>/errand`, as it does on a machine it was sent to. A relay between versions
needs it: an installer replaces the file that is typed while its agent holds jobs. What runs is then still what it was -- it
starts what it needs of itself ( a container's agent, a Slurm step, `$ERRAND` in its runs ) from that copy, it is named by it --, the
next command finds another hash, another agent, and the place is relayed to it. On Windows, a program that runs could not have been
replaced at all. A copy nothing used for a week goes ( the rounds ). What a distribution carries is found beside the program that was
typed, which the agent is told ( `ERRAND_TYPED` ).

## Python is left with declarations and a binding ( Hugo, 2026-11-25 )

ADR 0034 said Python keeps two things. It is so now: `lib/errand`, 2,900 lines, the
standard library, Python 3.8 or later.

| what stays in Python | why |
|---|---|
| `config`, `layers`, `runner`, `serve`, `declared` | a project's declarations are Python: held as they are read, checked where they are written, put down as one document ( ADR 0031 ). A layer, a runner, a service is what can be said of it; nothing there does anything |
| `entries`, `steps`, `rows`, `local` | what a run written in Python says and asks ( ADR 0032 ). `require` and `download` ask `$ERRAND` |
| `memos` | it keeps Python's objects, by what Python's arguments hold |
| `held`, `discovery` | errand's own entries are read by importing their files |
| `preloaded` | a warm start is a fork of an interpreter |
| `probes` | a service's probe that is a function of its declaration |

**It is carried by the program** ( `go:embed`, 130 kB ), put in `<cache>/binding/<key>` by whichever program needs it there, and found
before any other package of that name by what reads declarations. A program and its Python are of one version, always; no
machine is sent the one apart from the other; the Python that reads a project needs nothing installed.  A run finds it last on its path, as before: a
project that is errand's own, or that pins another, has its own.

**What a page's services ask for is in Go**: the packages that describe services ( package `svc` ), what is fetched and declared,
the diagnosis, the checkouts one works on, the versions held. What was
installed ( `services-installed.json` ) is read by the program: no Python is asked for it. A declaration whose package changed since is not
started. A file of declarations that does not load hides nothing of the others, and is said. A project's push says where the copy
comes from, with the checkouts inside it ( ADR 0028 §3 ).

**What installs a Windows is in Go**; its recipes are the files of `internal/agent/windows`, carried by the program.

## A name that does not change, kept by whoever comes ( Hugo, 2026-11-25 )

On updating errand where a server runs: "Peut être qu'en fait, l'idée serait de ne jamais rien faire côté serveur (tout est géré par
le principe des relais), mais de voir ça côté client au moment où on relance". It was so for a client ( the relay keeps the service
and its port ), and not for what has no errand on its side: a restricted key forces a command *there*, and nothing there had a
name that lasts. So the machine's agent, as it starts, points `<cache>/bin/errand` at its own program ( `current.go` ), unless
the one it names is of a later release or version; the round leaves what it names. Two of one version ( a build being worked on ): the
last to start. Nothing is installed, and the key stays restricted: a full one, so that its client could send errand, is a shell.
