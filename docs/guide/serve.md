# Servers kept while used: errand serve

```bash
errand serve --gpus 1 --gpu-ram 10G -- ollama serve     # its address: the first connection starts it
errand serve                                           # the servers of this machine, and the ports kept for them
errand serve stop ollama                               # the server; its port stays
errand serve release ollama                            # the port, given back
```

Some work is a server: an LLM that answers an application, a viewer that serves the frames of a run.
It should start when somebody asks for it, never twice, stop when nobody uses it, and, on a machine
shared with computations, hold its share of the card only while it works.

`errand serve` does that. The server runs under a **keeper**, a process of this machine's agent that
outlives the command that asked for it: it is listed by `errand status`, stopped with the rest, and
never left behind. Its share of the machine goes through the [queue](./machine), like a run's.

- **Started on demand, never twice.** Two `errand serve` at once start one server.
- **Stopped when nobody uses it**: after `--idle` ( 10 minutes by default ) with no connection open
  and the server not busy.
- **Its share given back while it rests.** In use, its claim is firm. Idle, it is kept warm: a
  computation that needs the card asks it to go, and it goes.
- **One address, kept.** The port is the server's as long as its [lease](#a-port-and-until-when) runs, whether the server
  is up or not, so a client, a tunnel or a bookmark keeps working from one start to the next.

## A port, and until when

A request says everything that makes the server, and is given a port: **until its lease is over, that port is listened on, and leads
to that server or to nothing**. Nothing is started by the request. A connection that finds the server stopped starts it, and waits.

```text
$ errand serve --keep 2h -- ollama serve
ollama  http://127.0.0.1:24997  ( rests, kept 2 h )
```

- `--keep` says for how long after the last request or connection; a day, if it is not said; two hours at most on a machine shared with others.
- The agent stays as long as it holds a lease. Another agent ( another version of errand, or the next one after it was killed )
  opens the same ports: any errand command starts it.
- The number, between 20000 and 29999, stays this server's fourteen days after its last use: asked again, it is the same. `--port N` imposes
  one: refused, saying by whom it is held, if it is.
- What the machine does not have, or a program that does not exist, is refused at once. A server that does not start closes the connection; the
  reason is said by the next request, `errand serve` and the page, and the next connections are refused for two seconds, then twice as long each
  time, up to a minute. `--wait` starts it and waits until it answers.
- `--json`, for a program: `{ "url", "urls", "ports", "until", "again_in", "state", "why", "config", "label" }`; ask again after `again_in` seconds, or when a
  connection is refused. `errand serve release` gives it back ( `--now`: the server stops too ).

**Same definition, same server.** The definition is the command, the variables *written* ( `--var A=1` ), the directory ( `--cwd`, default: the home ),
the needs, the ports' names and fixed sides, the probes, the contents of `--version-of FILE`, `--server-version`. Two requests that agree on these have one
server, whatever their label, `--idle`, `--grace` or `--keep`: while several run, the longest counts. One that differs is another server beside, at
another port; nothing is stopped. A program that was updated is another server behind the same port.

The name is a label. `stop ollama` stops every server that carries it, `stop ollama~c7ae` the one of that hash. → [ADR 0040](https://github.com/errand-run/errand/blob/main/docs/adr/0040-a-server-is-what-is-asked-for.md)

## Two sides, errand in between

<svg class="port-diagram" viewBox="0 0 760 390" role="img" aria-label="Clients connect to the announced ports, where errand's keeper listens; the keeper relays to the server's ports">
  <text x="0" y="16" class="head">announced ports</text>
  <text x="0" y="33" class="dim">yours to fix, or chosen once and kept</text>
  <text x="590" y="16" class="head">the server's ports</text>
  <text x="590" y="33" class="dim">chosen at each start, or fixed</text>
  <rect x="0" y="62" width="170" height="34" rx="6" class="box"/><text x="12" y="84">curl, a script</text>
  <rect x="0" y="108" width="170" height="34" rx="6" class="box"/><text x="12" y="130" class="mono">ollama run</text>
  <rect x="0" y="154" width="170" height="34" rx="6" class="box"/><text x="12" y="176">your laptop, ssh -L</text>
  <rect x="0" y="214" width="170" height="34" rx="6" class="box"/><text x="12" y="236">a gRPC client</text>
  <rect x="0" y="282" width="170" height="34" rx="6" class="box"/><text x="12" y="304" class="dim">another account</text>
  <path d="M170 79 C 205 79, 210 120, 245 120" class="wire"/>
  <path d="M170 125 L 245 120" class="wire"/>
  <path d="M170 171 C 205 171, 210 120, 245 120" class="wire"/>
  <path d="M170 231 L 245 231" class="wire"/>
  <path d="M170 299 C 200 299, 205 270, 225 262" class="refused"/>
  <path d="M220 253 l 12 12 M232 253 l -12 12" class="cross"/>
  <rect x="250" y="50" width="260" height="270" rx="10" class="keeper"/>
  <text x="380" y="74" text-anchor="middle" class="head">errand ( the keeper )</text>
  <text x="270" y="152" class="dim">listens as long as it lives,</text>
  <text x="270" y="168" class="dim">server up or not</text>
  <text x="270" y="190" class="dim">holds a connection while it</text>
  <text x="270" y="206" class="dim">waits for its share of the card</text>
  <text x="270" y="262" class="dim">counts them: one open = in use</text>
  <text x="270" y="278" class="dim">closes other accounts' ( Linux )</text>
  <text x="270" y="294" class="dim">asks the probe: busy?</text>
  <text x="270" y="310" class="dim">starts and stops the server</text>
  <circle cx="250" cy="120" r="8" class="port"/><text x="266" y="104" class="mono">http 127.0.0.1:11435</text>
  <circle cx="250" cy="231" r="8" class="port"/><text x="266" y="236" class="mono">grpc :50051</text>
  <circle cx="510" cy="120" r="8" class="port"/><text x="494" y="104" text-anchor="end" class="mono">{port:http}</text>
  <circle cx="510" cy="231" r="8" class="port"/><text x="494" y="236" text-anchor="end" class="mono">{port:grpc}</text>
  <path d="M518 120 L 590 120" class="wire"/>
  <path d="M518 231 L 590 231" class="wire"/>
  <rect x="590" y="90" width="170" height="170" rx="6" class="box"/>
  <text x="602" y="114">the server</text>
  <text x="602" y="138" class="dim">listens on 127.0.0.1,</text>
  <text x="602" y="154" class="dim">on the ports errand</text>
  <text x="602" y="170" class="dim">gave it; only errand</text>
  <text x="602" y="186" class="dim">connects there</text>
  <text x="602" y="216" class="dim">runs with the cards</text>
  <text x="602" y="232" class="dim">granted ( CUDA_VISIBLE_</text>
  <text x="602" y="248" class="dim">DEVICES... )</text>
  <path d="M380 320 L 380 342" class="link"/>
  <rect x="250" y="342" width="260" height="46" rx="6" class="box"/>
  <text x="380" y="361" text-anchor="middle">the machine's queue</text>
  <text x="380" y="378" text-anchor="middle" class="dim">its claim: firm in use, warm at rest</text>
</svg>

Each port of a service has two sides.

- **The announced side** is where clients connect. errand listens there itself, from the first
  `errand serve` until the service stops, whether the server is up or not. A connection that comes
  while the server waits for its share of the card is accepted and held, then relayed once the
  server answers. This side is yours to fix ( `11435` ), or errand chooses it once and keeps it for
  the name.
- **The server's side** is where the server listens, on 127.0.0.1. Only errand connects there. errand
  gives it to the server as `{port:NAME}` in its command and its environment, chooses a free one at
  each start, or takes the one you fix.

errand sits between the two to see every connection: that is how it knows the server is used
whatever its protocol, holds connections while the card is taken, and closes those from other
accounts.

Both sides can be fixed, but they cannot be the same number: errand listens on one and the server on
the other. To have the server where its clients expect it ( Ollama's `11434` ), fix the
**announced** side to that number, and let the server's be chosen.

## All of it, in the request

```bash
errand serve --name ollama --gpus 1 --gpu-ram 10G --idle 10m --busy-http /api/ps:models \
             --var OLLAMA_HOST=127.0.0.1:{port} -- ollama serve
```

There is no file of declarations: two requests that say the same are given the same server. Keep the line in a script, or say it
from a program.

### From a program

```python
import errand, requests

ollama = errand.serve( [ "ollama", "serve" ], on = "lmo", vars = { "OLLAMA_HOST": "127.0.0.1:{port}" }, gpus = 1, gpu_ram = "10G",
                       busy = errand.http( "/api/ps", field = "models" ), idle = "2m", keep = "1d" )

requests.post( ollama.url( "/api/chat" ), json = { ... } )                        # the url before each request: it costs nothing
ollama.call( lambda url: requests.post( url, json = { ... } ), "/api/chat" )      # ... and once more if its connection is refused
```

→ [The Python API](/reference/python-api#services). From Node, the same words ( `lib/node`, `errand-lib` ):

```js
import { serve, http } from "errand-lib";

const ollama = serve( [ "ollama", "serve" ], { on: "lmo", vars: { OLLAMA_HOST: "127.0.0.1:{port}" }, gpus: 1, gpu_ram: "10G",
                      busy: http( "/api/ps", { field: "models" } ), idle: "2m", keep: "1d" } );

const r = await ollama.call( url => fetch( url, { method: "POST", body } ), "/api/chat" );   // url(), port(), state(), wait(), stop(), release()
```

Any other language does the same with one command, `errand serve --spec - --json`: the request on its standard
input, its answer on the output, `{ "error" }` when it is refused.

```json
{ "command": [ "ollama", "serve" ], "on": "lmo", "vars": { "OLLAMA_HOST": "127.0.0.1:{port}" }, "gpus": 1, "gpu_ram": "10G",
  "busy": { "http": "/api/ps", "field": "models" }, "ports": [ [ "port", null ] ], "idle": "2m", "keep": "1d", "label": "ollama" }
```

The other keys: `cwd`, `ram`, `cpus`, `exclusive`, `others`, `listen`, `always`, `hold_end`, `ready`, `grace`, `version`, `version_of`; `on` is also a
list of layers, as data. The answer: `url`, `urls`, `ports`, `address` ( where they are reached from here ), `until`, `again_in` ( seconds: ask again
then, or when a connection is refused ), `state`, `why`, `config`, `label`, `on`.

`others`, `listen`, `always`, `hold_end` and a fixed announced number are of the port, not of the server: two requests that differ by
them have two ports, to one server.

A server takes its own share of the machine, beside the one of the run that asks for it: a run that holds all of a machine would wait for
a server that waits for it.

`{port}`, in the command and in the variables, is the server's side of the port; `$PORT` holds it too. The
cards granted are given as a run gets them ( `CUDA_VISIBLE_DEVICES`... ).

## Several ports, named

A server that listens on several ports names each of them; the command gets each one's server side
as `{port:NAME}`, and the environment as `$PORT_NAME`:

```bash
errand serve --name triton --gpus 1 --port http=8000 --port grpc=8001:18001 --port metrics= -- \
       tritonserver --model-repository /models --http-port {port:http} --grpc-port {port:grpc} --metrics-port {port:metrics}
# triton  http http://127.0.0.1:8000  grpc http://127.0.0.1:8001  metrics http://127.0.0.1:23817  ( rests, kept 24 h )
errand serve triton --url grpc      # http://127.0.0.1:8001  ( by its name, once it was asked for )
```

A port is `ANNOUNCED:SERVER`, either side left empty for errand to choose ( `:18001`, `8000:` ). A single port without a name is called `port`, and `{port}`
is its server side; with several, `{port}` alone is refused, and so is a `{port:NAME}` that names
none of them, when it is asked for rather than when it starts. A fixed server side that is
already taken stops the start with `port grpc: the server's side, 18001, is taken`.

The service is in use as long as a connection is open on any of its ports; `ready`, by default, is
every port answering.

## Only your connections

On 127.0.0.1, a port is open to every account of the machine. errand closes, at once, a connection
that comes from another account: on Linux it reads, in `/proc/net/tcp`, which uid opened it. A tunnel
of yours ( `ssh -L`, `errand serve connect` ) is yours, and passes. `keeper.log` notes each one closed.

With `--others`, every account of the machine may connect. Elsewhere ( macOS, Windows ) errand cannot tell, and relays every
connection; the server's own access control is then the protection.

The server's side is not guarded this way: another account that finds it connects to the server
directly. It changes at each start, unless fixed. A server that can listen on a Unix socket, or run
in a network namespace of its own, closes that door too; errand does not do it for you yet.

## Other machines: --listen

```bash
errand serve --listen 0.0.0.0 --gpus 1 --var OLLAMA_HOST=127.0.0.1:{port} -- ollama serve
```

The port is listened on at that address too ( `0.0.0.0`: every one of the machine; `100.64.1.2`: that one, a VPN's say ), and other
machines may connect: nothing tells who they are, the server's own access control is the protection. It is asked for each time,
never by default; a machine shared with others refuses it. Without it, the same request has its own port, at 127.0.0.1, to the same
server. A port listened on at one address is reached there from the machine itself too: the answer's `address` and `url` say it.

## A port the system keeps: --always

A program that asks errand ( the libraries, `errand serve` ) wakes it: an agent that was stopped, a machine that was restarted, it
starts one again, which listens on the same ports. A client that only has the number ( a browser, a container's configuration )
wakes nothing.

```bash
errand serve --always --keep 30d --name ollama -- ollama serve
```

`--always` hands the port to the system ( launchd on macOS, `systemd --user` on Linux ): it listens in errand's stead, and at a
connection starts `errand serve kept-lease`, which starts an agent if there is none and relays the connection to it. No agent
waits for it between two uses. When the lease is over or given back ( `errand serve release` ), the system lets the port go. Asked
for each time; refused on a cluster, or on a machine shared with others ( ADR 0028 §6 ).

## Ollama, from start to answer

Clients talk to the **announced** port, never to
`{port}`: that is the server's side, and changes at each start.

```bash
$ errand serve --name ollama [...] -- ollama serve
ollama  http://127.0.0.1:24997  ( rests, kept 24 h )

$ curl -s http://127.0.0.1:24997/api/generate \
       -d '{ "model": "qwen3:14b", "prompt": "Why is the sky blue?", "stream": false }'
{"model":"qwen3:14b","response":"Sunlight scatters off air molecules...", ...}
```

`--url` prints only the address, for a script; the first connection starts the server:

```bash
export OLLAMA_HOST=$( errand serve ollama --url )     # http://127.0.0.1:24997, the same as long as its lease runs
ollama run qwen3:14b "Why is the sky blue?"            # the ollama command line is a client too
```

From Python, with the `ollama` package or anything that speaks HTTP:

```python
import errand, ollama

server = errand.serve( [ "ollama", "serve" ], vars = { "OLLAMA_HOST": "127.0.0.1:{port}" }, gpus = 1, gpu_ram = "10G" )
client = ollama.Client( host = server.url() )
print( client.generate( model = "qwen3:14b", prompt = "Why is the sky blue?" )[ "response" ] )
```

From your laptop, the same with `--on lmo` ( [below](#on-another-machine-in-an-environment) ). What follows, on lmo's card:

```text
waiting      the card is held by a computation: the claim waits its turn, a request waits with it
starting     ollama serve, until its port answers ( a few seconds of GPU discovery )
in use       a request is open: the claim is firm
busy         no request open, a model still loaded ( OLLAMA_KEEP_ALIVE, 5 min by default )
warm         nothing loaded: a computation that needs the card evicts it, in about a second
stopped      after `idle` warm, or evicted
```

`OLLAMA_KEEP_ALIVE` sets how long a model stays loaded after its last request, and so how long the
server stays `busy`; `idle` counts after that. Point `OLLAMA_MODELS` at the models already pulled
( `/usr/share/ollama/.ollama/models` for a system install ) to use them without a copy.

## How errand sees that it is used

Every connection goes through the keeper: errand listens on the announced port and relays each
connection to the server. So it counts the open connections itself, on every system.

- **A connection open** means used, silent or not: a server that thinks for an hour before
  answering is not abandoned.
- **The probe saying busy** means used, with no connection: Ollama with a model loaded. `--busy-http /api/ps:models`
  asks the server for that path and looks at a field of its JSON ( `a.b` goes inside; busy unless it is missing, false, 0 or
  empty ), `--busy-if` is a shell command that exits 0 when busy. `--ready-http` and `--ready-if` say, likewise, that it answers. **A probe that fails or does not answer counts as busy**: a
  saturated server is in the middle of something.
- Neither: the `idle` clock runs, and starts again from zero at the next connection.

Throughput is never looked at. A client that keeps a connection open for nothing ( an HTTP
keep-alive ) keeps the server longer: the risk is taken on that side, never killing a computation.

## Waiting for its share

A service that waits for a card waits in the queue like any run, in order: the queue is first come,
first served, for everybody ( a run that takes no card may pass it, with what it leaves of the
cpus and memory the service asked for ). Meanwhile its port is already open: a connection that arrives is
accepted and **waits**, then is relayed once the server answers. The keeper gives up the wait when
nobody waits for it any more ( no connection, no `errand serve` waiting ) for `idle`.

## It stops

- after `idle` without connection nor busy;
- warm, when a computation needs its share ( `errand queue` shows it `kept warm: gives way` );
- with `errand serve stop NAME` ( its port stays: the next connection starts it again ), or Ctrl-C under `errand serve -f`;
- when the server ends by itself. It is not restarted: the next connection starts it again.

It is stopped as everything errand starts: INT, then TERM, then KILL after `grace` ( 10 s ), on its
whole process group. Then its claim is released.

## Those who may not say a command: authorizations

A page, and an ssh key meant for one server, must not send a command. Both give a **hash**, which the machine that serves
knows or refuses ( ADR 0040 ). What is authorized is written on that machine, in full, in `~/.config/errand/authorized-services.json`
( its user's alone ):

```bash
errand serve authorize --to key:boucles --on lmo --gpus 1 --idle 2m --var OLLAMA_HOST=127.0.0.1:{port} -- ollama serve
# 3f2a91c0  ollama authorized to key:boucles on lmo   ollama serve
errand serve authorized --on lmo              # what is authorized there, to whom, since when, last used
errand serve withdraw --on lmo 3f2a91c0       # to no one any more ( --to key:boucles: from that one only )
```

- The authorization sets everything: the command, the needs, `--idle`, `--keep`. The asker gives only the hash. It is the hash of the
  definition as that machine reads it: the same as `errand serve --on lmo … -- ollama serve` gets, and so the same server.
- `--to key:NAME` for a key; `--to pages --spec -` for a page's description ( below ).
- What you ask for yourself, by your own link, does not go through it.
- Values of variables named `…TOKEN`, `KEY`, `SECRET`, `PASSWORD` are not shown, by `authorized`, `status` and the page.
- A withdrawal cuts no connection already open.
- The page shows them in the Machines tab: `withdraw` on one. A refusal is written nowhere; a refused key leaves a line in the
  agent's journal.

## Through ssh: connect

```bash
errand serve connect 3f2a91c0
```

ensures the server of that hash, then relays this process's stdin and stdout to it ( `connect HASH PORT` for another port than
the first ). The hash is an authorization's, or the one `errand serve --json` gives ( `config` ) for what you asked for yourself.
It is what a key in `authorized_keys` forces, for a tunnel that starts the server when it is used:

```text
restrict,command="~/.cache/errand/main/bin/errand serve connect 3f2a91c0" ssh-ed25519 AAAA… boucles
```

The client behind such a key needs no errand. A key whose other side has one is given `errand there --only key:NAME` instead: a link
of errand's, on which that errand may say `hello` and reach, by their hash, the servers authorized to `key:NAME`, and nothing else
( no run, no file, no other machine, no errand sent to replace this one ). The filter is on the machine that serves:

```text
restrict,command="~/.cache/errand/main/bin/errand there --only key:boucles" ssh-ed25519 AAAA… boucles
```

That path is [errand's stable name](./layers/ssh#under-a-name-that-does-not-change) on a machine it was sent to: nothing is
installed there, and it follows the version the clients send.

- **The input ends** ( `printf … | ssh lmo`, `nc -N` ): the end is passed on to the server, as the half-close of a TCP
  connection ( `SHUT_WR` ), and the answer is relayed until the server closes. The same goes for the announced port. A server
  that cancels on it wants `--hold-end` ( below ).
- **While the service starts**, what comes is kept, its end included, and passed on once the server answers.
- **The client leaves** ( killed, Ctrl-C, or nobody reads the output any more ): `connect` ends, and its connection is no
  longer counted, whether the service starts or runs. Windows cannot tell that nobody reads.

### A server that cancels: --hold-end

TCP says with the same packet « I send no more » and « I am gone ». A server has to guess, and Go's `net/http` takes it for a
client that left: it cancels the request. Ollama is one: with a model loaded it answers `context canceled`; with none loaded
it never answers, nor closes ( 0.40.16 ). errand has no part in it: `nc -N` straight to Ollama does the same.

```bash
errand serve connect 3f2a91c0 --hold-end 30s
```

`connect` then keeps the end of its input until the server has answered, then been silent for 30 s; it may think as long as
it wants before. Choose it longer than the longest silence in the middle of an answer. It is passed on in the end, so that a
server that keeps its connections open ( HTTP/1.1 ) closes this one, which is no longer counted; with `Connection: close` in
the request, the server closes by itself, at once.

Each connection says it for itself ( in the key's line, for a forced command ): the server is not concerned, nor restarted, and
other connections to it do as they want.

A port can say it too, for all of its connections: `errand serve --hold-end 30s … -- ollama serve` is another port to the same
server. On a port that does not say it, the end is passed on at once: there, it is how the server knows that a client left, and
stops what it asked.

### While errand is updated

A server stays with the agent that started it until it stops, and its port goes to the new version's agent: an update cuts no connection.
Started again, it is the new version's, behind the same port. One that is always in use stays
with the version it began with: `errand serve stop NAME`, and the next that asks starts it again.

## On another machine, in an environment

```bash
errand serve --on lmo --gpus 1 --gpu-ram 10G -- ollama serve     # http://127.0.0.1:27311, on this machine
errand serve --env jax-lmo -- python viewer.py --port {port}     # ... in an environment of the project
```

The port is on **this** machine, whichever the server runs on. Each connection goes by the link errand holds to that machine: no
tunnel to set up, no new ssh login. Its first one starts the server there, which waits in that machine's queue if need be. What the
machine lacks ( the program, the cards ) is said at once; a machine that is away keeps its port, whose connections are closed
meanwhile.

The server belongs to the agent of its machine, which counts everybody's connections: two machines that ask for the same
one share it, under one account. `errand serve stop` from any of them stops it; `errand serve list --host lmo` says how it is there.

`--env` takes an environment of the project in full ( it is part of the definition ): `Ssh` first, then `Vars`, `Module`, `Venv`,
`Uv`, `Micromamba`, `Conda`. Containers, Slurm and Windows: not yet. A machine's own environments have to be there: a server
builds none ( `errand setup` does ).

**Its surroundings** are those of a login shell of its machine, then the layers, then the variables written ( `--var` ): never those
of the terminal it was typed in, nor of what started errand's agent.

## Seen

```text
$ errand serve
  ollama               busy       127.0.0.1:11435 1 connection  kept 23 h 59
$ errand status
running   ( on lmo: this machine )
  lmo: service ollama busy  1 connection
$ errand queue
  ollama ( service )  gpus=1, gpu_ram=10G  gpu 0  12 min  pid 4121
```

## errand hold

A claim held for a process that is not a run, for as long as its input stays open:

```bash
errand hold --gpus 1 --gpu-ram 10G -n "my server"
```

prints `granted 0` ( the cards ) once it has it, reads `warm` ( it may give way ) and `busy` ( it may
not ) on its input, prints `evict` when a computation needs the room while it is warm, and releases
when its input closes. `errand serve` is built on the same claim; `hold` is for whoever wants to
drive it themselves.

## A page that asks for a service

A run's page served as it is ( `errand.Static`, see [Steps](./steps) ) can ask for services: a
renderer that draws the current view, a field computed on demand. It describes each one in full, as a
program does for Ollama, and gets an address, or a refusal that says why and where to set it up
( ADR 0026, 0041 ).

### Opening it

A run that writes such a directory names it ( `errand.view( dir, open = errand.Static( "index.html" ) )` ), and
`errand run` prints its address beside the run's: the token is in the path, so it opens from the terminal as it is.
A directory already written opens with

```bash
errand open path/to/scene.sviz
```

which serves it beside `errand web` ( the host agent started if need be ) and opens it in the browser. Opened as a
file, its page cannot read the files beside it: it shows that line, with its path.

### The page describes its service

```js
const machines = await fetch( ".errand/machines" ).then( r => r.json() );    // [ { id, name, here } ]
const r = await fetch( ".errand/srv", { method: "POST", body: JSON.stringify( {
  on: machines[ 0 ].id,                                                         // none: the machine that serves the page
  name: "blender", protocol: 1, text: "Pictures of the current view",
  run: [ "{tool:blender}", "-b", "--factory-startup", "-P", "{file:blender.py}", "--", "--port", "{port}" ],
  files: { git: "https://github.com/sdot-team/sdot", rev: "807e184", dir: "src/sdot/viz/services" },
  tools: { blender: { min: "5.0", look: { "macos-*": [ "/Applications/Blender.app/Contents/MacOS/Blender" ], "*": [ "blender" ] },
                      version_from: [ { plist: "../Info.plist", key: "CFBundleShortVersionString" }, { dirname: "blender-(\\d+\\.\\d+\\.\\d+)" } ],
                      fetch: { "linux-x86_64": { url: "https://download.blender.org/…/blender-5.2.2-linux-x64.tar.xz", sha256: "8409…",
                                                 run: "blender-5.2.2-linux-x64/blender", version: "5.2.2" } } } },
  needs: { gpus: "0-1" }, idle: "10m" } ) } );                                // a card if the machine has one
const got = await r.json();
if ( r.ok ) fetch( got.url + "render?frame=3" );                            // .errand/srv/9c41e2d0/blender~5c1e09ab/
else panel.innerHTML = got.html;                                             // why, what the machine has and lacks, a button to set it up
```

The description says no path of any machine. Its placeholders, a closed set, are resolved on the machine that serves:

- `{tool:NAME}`: a program found there ( `look`, the `PATH`, errand's cache, a path a person gave ), its version read
  from files ( `version_from`: `Info.plist`, a directory's name, dpkg, snap ) and at least `min`; else fetched ( `fetch`,
  per platform: URL, sha256, the program inside );
- `{file:REL}`: a file of `files`, a repository at a commit ( only `dir` fetched, into errand's cache ) or a checkout of yours
  ( `dev: true` ); nothing in it is installed;
- `{python}`: an env of the service's own, with what `python.install` says, in one `pip install`: requirements of the index
  ( `"numpy>=2"`, `"sdot==2026.11.3"` ), and repositories, at a commit ( `git+URL@REV#subdirectory=PACKAGE` ) or as a checkout of
  yours ( `-e`, edited as it runs ). A repository given provides its package: pip does not take the index's ( your loom, in place
  of PyPI's `loom-ffi` ):

  ```js
  python: { install: [ { git: "https://github.com/sdot-team/sdot", dev: true },
                       { git: "https://github.com/hleclerc/loom", dev: true, package: "python" }, "numpy>=2" ] }
  ```
- `{port}`, `{port:NAME}`: the server's side of its port.

The other keys: `vars`, `needs` ( `gpus`, `gpu_ram`, `ram`, `cpus` ), `busy`, `ready`, `idle`, `grace`. A repository
in development may say `version_of`: its files whose edit makes another server ( `"src/sdot/viz/**/*.py"`, relative to it ).
`gpus` may be a range, `"0-1"`: as many cards as the machine has, up to one, decided on each machine when it is authorized
( a Mac's GPU is no card errand counts: Blender takes it by itself, and the service runs with none claimed ). A
key errand does not know is refused: what a page means is never guessed.

Its **hash**, eight hex digits of all it says ( `on` aside ), is its identity, computed by errand: another commit is another
service. The address names the machine by an id ( `GET .errand/machines` gives them, with their names ), never by a name,
which can be ambiguous; the page keeps its choice.

The address stays valid as long as the authorization holds: there is no lease. A request that finds the server stopped
starts it. The relayed request carries **`X-Errand-Root`**: the directory as the service's machine sees it ( the original,
a shared file system, or errand's copy ), percent-encoded. Any method, a body, a WebSocket.

### A refusal, and the window that authorizes

`{ "error": "not authorized" | "no store" | "unavailable", "why", "html", "authorize", "form" }`, with 403:

- `html` is a fragment the page inserts as it is: why, what the machine has and lacks, and a button *Set it up on lmo…*: a
  form that POSTs the description, the machine and the view to `authorize`, **errand's own page**, in a new window. It holds no
  script, with classes ( `errand-ok`, `errand-missing`, `errand-setup` ) a page may restyle. A page that draws its own sends
  the fields of `form` there the same way. The page adds a *Retry* button. Nothing polls, and nothing is kept in between.

The window is errand's, as a card payment's is the bank's: another origin than the view's, errand's cookie, never framed;
another account of the machine that finds the address can do nothing there. It shows, for the machine chosen, one part per
thing the service needs, in one list, in the order things happen: what is already there ( green ), what is yours to say, a
checkout's path ( orange ), or cannot be there ( red ), what errand sets up once, when you authorize: a download, the code at a
commit, its env, the view's files ( blue ); then the service itself. Each says its state, and what the page says of it ( folded ):

- **the code**: the repository at its commit, in errand's cache; or, in development, a checkout of yours, whose path you give
  there ( the command it stands for, `errand serve dev REPO PATH`, is shown as you type, to copy );
- **its Python env**, for `{python}`: an env of the service's own, built by errand ( from the repository at its commit, or the
  checkout installed in it as you edit it, `pip install -e` ), kept and removed with the service ( `serve list --versions`,
  `prune` );
- **each tool**: the one found, with its version and path, a field for another one, *run --version once* for a program whose
  version no file says; or what will be downloaded;
- **what the machine cannot give**, only when it cannot: `"1 card at least"`, more memory than it has;
- **the view's files**: where the machine sees them, or that they will be copied;
- what is downloaded has its source, size and sha256. A source outside `errand.sources` is red, and needs a box ticked;
- the whole command, with that machine's paths;
- **Authorize**: fetches, copies the view's files if that machine does not see them ( into errand's cache there, kept up to
  date while the view is open ), composes, and writes the authorization `to: pages` in that machine's
  `authorized-services.json`.

Then *Retry* in the page gets the address. The window takes only a form from a view's own address, with errand's cookie;
what it shows comes in that form, and Authorize sends it back: nothing of a refusal is kept. The same from the command line,
the description on stdin:

```bash
errand serve authorize --to pages --on lmo --spec - < blender.json      # shown, then asked; --yes, --source URL, --path blender=/opt/b
```

- `errand serve fetch --on M URL#sha256=H`, `git:URL@COMMIT`, `--for NAME~HASH git+URL@COMMIT` fetch by hand, and
  `errand serve probe --on M -- PROG --version` reads a version once: what the window does, typed.
- The list of trusted sources holds PyPI, conda-forge and download.blender.org by default, and what you add:
  `errand serve source URL` ( `--off` ), in `~/.config/errand/config.json`. The repository of a package already installed
  here counts too.
- `errand serve authorized` lists what pages may ask for, and the page's Machines tab shows them; `withdraw` takes one back.

**Without errand** ( the directory under `python -m http.server`, or sent to someone ): `.errand/srv` answers 404, and
the page works on its files alone. By convention, a page then takes a service's address from its URL
( `?svc.field=http://127.0.0.1:9000` ), and a service started by hand is told its directory with `--root DIR`, since no
relay adds `X-Errand-Root`. Such a service answers CORS for `127.0.0.1` and `localhost`, because the page is on another port.

- **The queue is per user and per machine.** A server started under another account ( a systemd
  service ) is not seen, and its share of the card is not counted.
- **The server's side of a port is open to every account of the machine**, and so is the announced
  side outside Linux ( see [Only your connections](#only-your-connections) ).

### Developing a service

While working on a service, a new commit at each edit is in the way. On this file system, say that a
repository is represented by the checkout you work on:

```bash
errand serve dev github.com/sdot-team/sdot ~/Projects/sdot-ffi --env prj-jax      # --off: no more
```

A page that describes a repository with `dev: true` instead of a `rev` ( in `files`, or in `python.install` ) is then
authorized to run from that checkout ( the window of errand's page asks where it is, one part per repository ); `{python}` is
an env errand builds for the service, the checkouts installed in it as you edit them. The page chooses, per service and per repository, the
commit or your checkout ( each service has its env ): it is part of what that service is. The hash does not depend on the code: an edit of the files `version_of` names gives another
server behind the same address, at the next connection.

A copy of the project on another machine ( the copy errand keeps for the runs there ) carries it: the
checkout inside the copy is a dev source on that machine too, with `"envs": { "lmo": "lmo-jax" }` in `config.json` for the
env there. It is only as fresh as the last push. The storage tab lists the dev sources under their disk,
and `d` turns one off ( or on again ).

### What piles up, and goes

Each description authorized to pages ( `blender~63ecc287` ) brings its env and the code fetched for it. errand keeps track:

```bash
errand serve list --versions          # each: its size, when it was used
errand serve prune --dry-run          # what the purge would remove
errand serve pin blender~63ecc287     # kept whatever happens ( unpin to undo )
```

One not run nor authorized for 14 days, and not pinned, goes at the host agent's daily purge, with its env; the code
fetched for it goes with the last that needed it. `errand serve prune --unused DAYS` does it now. The storage tab of
`errand web` lists them under the disk of errand's directory, `d` removes one.

## Next

[Sharing a machine](./machine) · [Agents](./agents) · [What is running](./status)
