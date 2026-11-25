# Decisions

What was decided, and why. Those up to 0032 were taken while errand was written in Python, and speak of its files; what they decide stands,
unless a later one says otherwise. 0033 ( a port piece by piece ) was left for 0034. What was tried, and how, at each step of the new program
is in the history of the repository that was errand before this one.

| | | status |
|---|---|---|
| [0001](0001-verbs-path-targets-runners.md) | Verbs, path targets, runners ranked by suitability, a comma grid everywhere | accepted |
| [0002](0002-resolve-in-the-environment-two-phases.md) | Targets are resolved in the environment, in a preparation phase and a run phase | accepted |
| [0003](0003-run-launches-everything-output-on-request.md) | `run` launches everything, the output directory is asked for, entries become a protocol | accepted |
| [0004](0004-a-built-thing-keeps-its-own-record.md) | What was built is recorded where it lives, and described from both ends | accepted |
| [0005](0005-the-local-side-orchestrates-agents-execute.md) | The local side orchestrates, agents execute, and a hub keeps them alive | proposed |
| [0006](0006-files-cross-through-the-agents.md) | Files cross through the agents, by manifest, not through rsync | accepted |
| [0007](0007-a-grid-is-asked-for.md) | A grid is asked for, with its separator | accepted |
| [0008](0008-nothing-runs-unowned.md) | Nothing runs unowned: an owner on each machine, a ledger here | accepted |
| [0009](0009-arrivals.md) | Arrivals: what is asked for, may come late, and is owned meanwhile | accepted |
| [0010](0010-steps.md) | Steps: a run says where it is, and errand shows it live | accepted |
| [0011](0011-a-run-is-one-invocation-of-a-tool.md) | A run is one invocation of a tool; entries lose their needs | accepted |
| [0012](0012-cache-zones.md) | A cache zone: what both sides add to, and nobody replaces | proposed |
| [0013](0013-download.md) | `errand.download`: a file by its hash, in a cache errand declares itself | proposed |
| [0014](0014-a-run-asks-for-what-it-needs.md) | A run asks for what it needs: on the command line, or from its code | proposed |
| [0015](0015-memo.md) | `@errand.memo`: a result kept by its inputs, in the project's cache | proposed |
| [0016](0016-the-terminal-is-drawn-by-the-driving-side.md) | The terminal is drawn by the side that drives: blocks, a gutter, and lines that do not stay | accepted |
| [0017](0017-a-runs-frames-are-a-directory-followed-live.md) | A run's frames are a directory, followed live and opened by its viewer | proposed |
| [0018](0018-serve.md) | `errand serve`: a service the agent keeps, under the queue, until nobody uses it | proposed |
| [0019](0019-what-a-run-makes-is-not-errand-s-to-remove.md) | What a run makes is not errand's to remove; a library's caches are errand's to place | proposed |
| [0020](0020-a-library-cache-says-what-it-holds.md) | A library's cache says what it holds; errand removes what no longer serves | proposed |
| [0021](0021-no-rsync.md) | errand does not use rsync | accepted |
| [0022](0022-caches-are-looked-after-per-file-system.md) | Caches are looked after per file system, by the first machine that reaches them | proposed |
| [0023](0023-runs-as-a-tree-and-removed-by-hand.md) | The runs as a tree, their parameters as a table, removed by hand | proposed |
| [0024](0024-agents-say-what-changes.md) | Agents say what changes; nothing is asked again and again | proposed |
| [0025](0025-the-runs-listed-are-this-machines.md) | The runs listed are this machine's record | accepted |
| [0026](0026-a-page-asks-for-its-services.md) | A page asks for its services, and errand serves it through the page's own address | proposed |
| [0027](0027-a-pages-services-run-where-one-chooses.md) | A page's services run on the machine one chooses, as the installed packages describe them | accepted |
| [0028](0028-storage-dev-sources-and-kept-ports.md) | File systems are what errand looks after; services have versions to clean, sources to develop, ports to keep | accepted |
| [0029](0029-a-job-has-its-own-keeper.md) | A job has its own keeper; agents own no job | proposed |
| [0030](0030-copies-are-written-down-before-they-are-made.md) | A copy is written down before it is made, and removed on demand | proposed |
| [0031](0031-declarations-are-data.md) | Declarations are data: written in Python, read as one document | proposed |
| [0032](0032-what-a-run-says-is-a-protocol.md) | What a run says to errand is a protocol, not a library | proposed |
| [0034](0034-a-new-program-not-a-translation.md) | errand in Go is a new program, built from what errand must do -- not a translation | proposed |
| [0035](0035-a-place-has-an-agent-that-owns-its-jobs.md) | A place has an agent, which owns its jobs; five programs | proposed |
| [0036](0036-entries-are-listed-a-run-carries-several.md) | Entries are listed, a run carries several, and any tool's code asks for what it needs | proposed |
| [0037](0037-distributed-by-pip-built-on-purpose.md) | Distributed by pip, one wheel per system; built on purpose | accepted |
| [0038](0038-what-is-not-errands-stops-a-push.md) | What is not errand's stops a push; a place may have its cache; a journal is watched | proposed |
| [0039](0039-builds-are-in-errand-s-caches.md) | Builds go where errand keeps its caches; what is costly stays longer than what is not | accepted |
| [0040](0040-servers-of-one-name-coexist.md) | Servers of one name coexist: a service is its name and what makes its answers | proposed |
