---
status: proposed
date: 2026-11-25
---

# Servers of one name coexist: a service is its name and what makes its answers

ADR 0018 made a service one per ( user, machine, name ), so that a server is never started twice. Two things went against it.

- A server was given an option that only one of its clients needed ( the end of the input held back, 2026-11-25 ). As its declaration it
  took a restart of the server to be read, by everybody. It went to the connection ( `errand serve connect --hold-end` ).
- Measured the same day: `services.py` changed and the service asked for again, the server that runs is answered, with the
  configuration it was started with. An ad hoc one ( `errand serve --name N -- another command` ) is answered the same way, by the code.
  Nothing says so.

Hugo: "En principe, les serveurs avec des configs différentes peuvent coexister, et on ne doit jamais avoir à intervenir sur les
agents." Then, on how far that goes: "est-ce qu'on pourrait aussi ajouter les dépendances ? En fait, je ne sais pas jusqu'où aller. En
effet on peut imaginer 2 machines qui demandent 2 services complètement ou partiellement différents avec le même nom et une partie
commune dans la config."

ADR 0026 had a part of it: the services of a page are `name#version`, the version is a hash of files the producer lists, and versions
coexist. This is the same for every service.

## What there is ( checked in the code, 2026-11-25 )

- An agent finds a service by its name, in its own table, then in those of the other agents of the machine ( `holder` ). What was asked is
  looked at only if none runs.
- Asked for with `--host`, a service is read from the declarations of the machine it runs on: two client machines that give a name get the
  same one, and only an ad hoc command is sent from the client. No directory goes with the request ( `cwd` empty ), and what is read is then
  around **the directory the agent happens to run in**: the home on lmo, whose agents ssh starts; on the Mac, `~/Projects`.
- A probe that is a function is not data: the declaration says `{ "function": true }`, and a Python process on the service's machine reads
  the declaration **by its name** when the probe is first asked, and again if that process dies.
- The program is found on the `PATH` of the agent, when the server starts.
- The announced port is listened on by the service; one that is chosen is kept for the name ( `serving/<name>/ports.json` ).
- A claim kept warm gives way to any request that needs its share, another server's included.

## Decision

### The identity: what can change the answers, as far as errand can see it or is told

Too wide, a client is given the answers of another server, and nothing says so. Too narrow, there is one server too many: it is
seen, and it costs a card that two servers take from each other. The first is worse, so what can change an answer is in; the second is
real, so nothing else is.

| | in the identity | when two requests differ by it |
|---|---|---|
| **what the server is**: name, command, variables, directory, what it asks of the queue, fixed ports of the server's side | yes | two servers |
| **its probes**, `busy` and `ready`: the line of shell, the path, or the code of the function | yes | two servers |
| **its program**: the file the command's first word leads to, its size and its date ( a stamp, as ADR 0004's ) | yes, by itself | an Ollama that was updated is another server, for the next request |
| **what is declared**: `version_of = [ files ]`, by their contents; `version = 2`, as a `memo`'s | yes, if declared | two servers |
| **how it is kept**: `idle`, `grace`, the label, the announced ports, `others` | no | one server, see below |
| **the rest**: shared libraries, imported packages, models, drivers, the agent's own variables | no | errand does not follow them: raise `version` |

- The hash of these, shortened, is the server's **config**. It is computed at each request, on the machine the server runs on: a few
  `stat`, and the listed files read again when their date changed. Taken again just before the server starts.
- A request from another machine is read around the home directory, as a forced `connect` is: the same name must not be another config for
  having found another agent.
- For a `name#version` of ADR 0026, the key is the name, and the rest is the same.
- The code of a function is hashed by errand's Python as it reads the declaration: its source, else its bytecode. What it calls elsewhere is
  not followed.
- `python server.py` is the program `python`: the file goes in `version_of`. So does what an `env A=b …` or a `sh -c` starts.

**Equal or not; nothing in between.** errand cannot say which variables matter to a server: `OLLAMA_KV_CACHE_TYPE` changes the answers,
another may not. What two servers have in common is shared otherwise: files by naming the same directory; a process by making it
a service of its own, that both use.

### What a request asks of a server it shares

- `idle`, `grace`: the longest of what was asked since the server started.
- An announced port is the request's: each leads to the server, with its own `others`. Letting other accounts in by one port opens no other.
- The label is the first one's.
- What only concerns a client's connection is an option of that connection ( `--hold-end` ), never of the service.

### No succession, hence nothing to do

A configuration that changed is another server, started beside at the next request. The one before gets no new client by
name, and ends as any does: unused for `idle`, or asked for its share while it rests. Nobody stops it, no agent is told anything.

An update of errand changes no identity: the agent that comes next finds the server by its config with the agent before, as it does
today by its name. An agent says the config of each server it lists. **One that does not say it** ( a version before this ) is matched
by the name alone, as today, until its server ends.

`NAME#dev` ( ADR 0028 §3 ) keeps its own rule: a checkout that is edited stops the server of the code before, which nobody wants any more.

### A port follows the declaration, not the server

`connect`, `forward` and a page go to the config they asked for. A client of a port only has a number, and must never be led to a
server somebody else declared. So a port, fixed or chosen, belongs to **where the service is declared**: the user's `services.py`, a
project ( its root: today a declaration only says the directory it was read around ), an installed description, or, ad hoc, the config itself.

- The same place, with another configuration: the port leads to the new server from then on; connections that are open stay where
  they are. This is what keeps the address of a declaration one edits.
- Another place, asking for a number that is held: its server starts **without it**, and the command says who has it. It takes it when it
  is free.
- The listener is the agent's, not the server's. Held by an agent of another version, it is closed there and opened here ( the agent
  before keeps what it accepted ). An agent that does not know that request keeps the port until its server ends.

### Commands

- `errand serve stop NAME` stops every server of that name; `--config HASH` one of them.
- `errand status` and `errand serve` show the config when a name has several servers. `errand serve show NAME` says what went in each: it
  is how one understands why there are two.

### On disk

`serving/<name>/<config>/` for the journal and the server's record, `serving/<name>/ports.json` for the ports, by place of declaration.
Not `<name>#…`: that form is the versions', which are cleaned.

A function's probe is read with the config it is for. If its process dies and the declaration has changed since, the probe is gone: the
server is then in use by its connections alone. It is not counted busy as a probe that fails is: it would never give its share back.

## Later: the declaration of the client

Two machines that ask for two servers under one name have, for now, the ad hoc form. Next, with `--host`, a project of the client's may send
its declaration, which wins over the machine's as a project's does locally. Not a probe that is a function: it exists only where it was
read. A client that gives a name and nothing else, as a forced command does ( `connect ollama` ), keeps the machine's. To be decided
when it is done: whether a name should say that it is the machine's it wants.

Not now either, and not prevented: a service that says it needs others ( `needs = [ "ollama" ]` ).

## Considered options

- **The whole declaration** ( proposed first ): two machines that differ by `idle` would each have an Ollama.
- **The name and the command only**: a variable changes the answers as well.
- **Sharing when the configurations are compatible** ( "this one does not care" ): a language of constraints, and a server whose nature depends on
  who came first.
- **Following the dependencies** ( open files, libraries, packages ): different on each system, and still blind to what is read later.
- **The new server replaces the old**: whoever works with the old is cut.
- **The port leads to the last configuration asked for, wherever declared** ( proposed first ): two projects with one name would
  each be sent to the other's server, silently.
- **`SO_REUSEPORT`**: Linux spreads connections over both servers.

## Costs

- **Two servers that do not fit together take turns.** Two Ollama of 10 GB on an 11 GB card: the second waits until the first rests, a minute
  after its last request with `OLLAMA_KEEP_ALIVE=1m`. Two clients in turn load the model again each time.
- A connection nobody reads any more holds its server, and the card, whatever the configuration.
- A dependency one forgot to declare: the old server answers until it stops for want of use.
- A port that goes from one agent to another is closed for an instant: a connection that arrives then is refused.
- What is declared is hashed at each request.

## Steps

1. The config: in Go what it is made of, the function's code from errand's Python; `version_of`, `version`; tests of what does and does not
   change it.
2. Found by ( name, config ), across agents; an agent that does not say it. `idle` and `grace` shared. Directories.
3. The agent's listeners; the port by place of declaration; handed from an agent to another.
4. `stop`, `status`, `serve show`; the guide, the API reference, the skill.
5. Later, the client's declaration.
