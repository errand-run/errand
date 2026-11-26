---
status: accepted
date: 2026-11-25
amends: 0018, 0026, 0027, 0028
amended-by: 0041
---

# A server is what is asked for, and is given a port on the asker's machine

ADR 0018 made a service one per ( user, machine, name ), declared on the machine it runs on. Two things went against it.

- A server was given an option that only one of its clients needed ( the end of the input held back, 2026-11-25 ). As its declaration it
  took a restart of the server to be read, by everybody. It went to the connection ( `--hold-end` ).
- Measured the same day: `services.py` changed and the service asked for again, the server that runs is answered, with the
  configuration it was started with. Nothing says so.

A first version of this ADR ( servers of one name coexist, their ports given by where they are declared ) was left for this one. Hugo: the agent
then still decides what its user may want; better is to ask errand for a port, with *all* of what makes the server, and to be given the one
that server has, if it has one. Settled in a grill on 2026-11-25 ( `grill/services/decisions.md`, three rounds ).

## Decision

### The request defines the server

A request says everything: there is no declaration to look up. `services.py`, `errand.services` and `errand.Service` go, in the
project's files too.

```python
ollama = errand.serve( [ "ollama", "serve" ], on = "lmo", vars = { "OLLAMA_HOST": "127.0.0.1:{port}" }, gpus = 1, gpu_ram = "10G",
                       busy = errand.http( "/api/ps", field = "models" ), idle = "2m", keep = "1d", label = "ollama" )
requests.post( ollama.url( "/api/chat" ), json = … )
```

| | |
|---|---|
| **in the definition** | where it runs ( `on`: a machine, or layers written in full ), command, variables *written*, directory, needs, the names of its ports, the fixed ports of the server's side, probes, `version_of` ( contents ), `version` |
| **of the port** | `others`, `listen`, an imposed number, `hold_end`: requests that differ by these have two ports, to one server |
| **of the request** | `keep`, `idle`, `grace`, the label |
| **never** | the surroundings of the terminal it is typed in: two terminals would be two servers |

- Two requests share a server when their definitions are equal, once normalized ( `10G` is `10240M`, the order of variables does not matter ). Not
  in part: errand cannot say which variables matter to a server. Two clients that want to share write the same thing.
- A server starts in the surroundings of a login shell of its machine, then the layers, then the variables; not those of the agent, which
  depend on what started it.
- A **server** is a definition and the program found for it ( path, size, date ); a **port** is the definition's. A program that was updated is
  another server behind the same port: the one before keeps its connections, and ends by itself.
- Nothing replaces or stops another. Equal or not, nothing in between; no succession; no agent is told anything.
- Probes are data: a line of shell, or an HTTP path and a field. No Python is kept to answer them.
- The name is a label, for `status`, `stop` and the page. `stop LABEL` stops all that carry it, `LABEL~hash` one.

### A request is given a lease

`{ ports, until, again_in }`. **Until `until`, the port is listened on, and leads to that server or to nothing**: never to another. A
connection that finds the server stopped starts it, and waits.

- `keep` says for how long after the last request or connection; a day, if it is not said. Two requests: the later end. A machine shared
  with others: two hours at most, and the answer says so.
- The server stops as it did ( `idle`, or its share asked for while it rests ): the port stays. `idle` and `grace` are the longest of the leases
  that are running.
- The agent stays while it holds a lease. Leases are on disk: the next agent opens the same ports, be it of another version ( the one before
  closes its listeners and keeps what it accepted ).
- A number stays the definition's for fourteen days after its last use. It is taken in 20000–29999: outside what systems give
  to whoever asks for any port ( 49152 and up on macOS, 32768–60999 on Linux ). An imposed number that is held is a refusal that says by whom.
- The port is always **on the asker's machine**. Where the server is elsewhere, the agent here relays each connection by the link it has.
  `forward` goes. The server belongs to the agent of its machine, which counts everybody's connections: that is how two machines share one.
- Nothing is started by a request ( `--wait` does ). The program and the needs are looked at there and then.
- A server that does not start: the connection is closed, the reason is in its journal and on the page; the next are refused for two seconds, then
  twice as long each time, up to a minute.
- `stop` stops the server, its port stays. `release` gives the lease back; `--now`, both.

### Who asks again

The libraries ask before *each* request, from memory: errand is asked again at half of the lease, and once more when a connection is
refused ( a machine that was restarted, an agent that was killed ). Asking starts an agent when there is none. What is kept is in
memory only: an old number does not outlive the program. They are thin: the way in is `errand serve --json`.

A client that only has the number ( a browser ) is woken by nothing, unless the system listens in errand's stead: `--always`, asked for, never on a
cluster ( ADR 0028 §6 ).

### Those who may not ask for anything

A page ( ADR 0026 ) and an ssh key meant for one server must not send a command. Both give a **hash**, which the machine that serves
knows or refuses: `~/.config/errand/authorized-services.json`, the definition in full, to whom, since when.

- `errand serve authorize --to pages|key:NAME [--idle --keep] -- …`: the authorization sets everything, `keep` and `idle` included.
- A page keeps giving the portable hash its package has everywhere ( the full one has paths of a machine ); the authorization leads from it to
  a definition, whose hash is the server's. The page's version is the definition's `version`: two versions are two servers, whatever runs them.
- A key: `restrict,command="… errand there --only key:NAME"`. The agent takes, by a list of what is allowed, nothing else than these services.
  Its client cannot send its errand: it speaks to the one that is there, or says that they do not understand each other. Without errand at
  all: `errand serve connect HASH`.
- The page shows the authorizations, their use, what was refused lately; takes one back; gives one, the whole command in the confirmation ( it checks
  the cookie and the origin, and views are of another origin ).
- What a user asks for, by their own link, does not go through it.
- Leases and authorizations are files of their user alone ( 0600 ). Values of variables named `…TOKEN`, `KEY`, `SECRET`, `PASSWORD` are not shown.

`dev_sources` and `sources` go to `~/.config/errand/config.json`, as data.

## Considered options

- **Servers of one name, by declaration** ( the first version ): the port's owner, whose declaration wins, which agent matches a name — rules that
  exist only because the server is looked up rather than said.
- **Sharing when definitions are compatible**: a language of constraints, and a server whose nature depends on who came first.
- **A common file of definitions, or errand saying what differs**: not for now; two clients write the same thing.
- **A fixed number of days**: how long a port is needed is known by who asks.
- **A shared kind of run**: the agent of a machine owns its servers already.
- **errand in the client's container with a full key**: it could ask for any command.

## Costs

- Going through `url()` rather than keeping a string. Who keeps the string has `keep` before it.
- An agent that waits, for as long as a lease runs: some tens of MB.
- The first request waits for the start, and is the one that fails if it does.
- Two servers that do not fit together take turns, if their definitions differ.
- Not covered: containers, Slurm nodes, Windows.

## Steps

1. A lease, on one machine: the definition and its hash, the port, `keep`, taken over by the next agent, `stop` / `release`, `--json`, the page.
2. `on` ( another machine, layers ), the login shell's surroundings, probes as data; `services.py` and `forward` go.
3. The Python library.
4. Authorizations, the pages, `there --only`, `connect HASH`.
5. `--listen`, `--always`, Boucles, Node.

No compatibility is kept between them.
