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
first served, for everybody. Meanwhile its port is already open: a connection that arrives is
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
errand serve authorized --on lmo              # what is authorized there, to whom, since when, last used; what was refused lately
errand serve withdraw --on lmo 3f2a91c0       # to no one any more ( --to key:boucles: from that one only )
```

- The authorization sets everything: the command, the needs, `--idle`, `--keep`. The asker gives only the hash. It is the hash of the
  definition as that machine reads it: the same as `errand serve --on lmo … -- ollama serve` gets, and so the same server.
- `--to pages NAME#VERSION` is for pages ( below ); `--to key:NAME` for a key.
- What you ask for yourself, by your own link, does not go through it.
- Values of variables named `…TOKEN`, `KEY`, `SECRET`, `PASSWORD` are not shown, by `authorized`, `status` and the page.
- A withdrawal cuts no connection already open.
- The page shows them in the Machines tab, with what was refused lately: `withdraw` on one; `authorize to pages` on a page's request
  that was refused, its whole command shown before.

## Through ssh: connect

```bash
errand serve connect 3f2a91c0
```

ensures the server of that hash, then relays this process's stdin and stdout to it ( `connect HASH PORT` for another port than
the first ). The hash is an authorization's, or the one `errand serve --json` gives ( `config` ) for what you asked for yourself.
It is what a key in `authorized_keys` forces, for a tunnel that starts the server when it is used:

```text
restrict,command="/.cache/errand/bin/errand serve connect 3f2a91c0" ssh-ed25519 AAAA… boucles
```

The client behind such a key needs no errand. A key whose other side has one is given `errand there --only key:NAME` instead: a link
of errand's, on which that errand may say `hello` and reach, by their hash, the servers authorized to `key:NAME`, and nothing else
( no run, no file, no other machine, no errand sent to replace this one ). The filter is on the machine that serves:

```text
restrict,command="/.cache/errand/bin/errand there --only key:boucles" ssh-ed25519 AAAA… boucles
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
renderer that draws the current view, a field computed on demand. Each one may run on the machine
the person chooses in the page: here, where the run wrote the directory, or any machine errand
knows ( ADR 0026, 0027 ).

### The package says how a service runs

The code of a service never comes from the page. By default it comes from the producer's
repository, at the commit that wrote the directory, into an env of that service's own on the
machine ( `<cache>/svc-envs/<name>@<version>` ). errand never runs a service in an errand env nor
in one of your envs as it is. You may put your own checkout in the service's env
( `pip install --no-deps -e` ). The package ships, beside the code, a file of data,
`errand-services.json`, and names its module by an entry point:

```toml
[project.entry-points."errand.services"]
mytool = "mytool.services"
```

```json
{ "format": 1, "package": { "name": "mytool", "pip": "mytool" },
  "services": {
    "field": { "protocol": 1, "text": "Computes a field of the scene on demand.",
               "version_of": [ "field.py" ],
               "run": [ "{python}", "-m", "mytool.services.field", "--port", "{port}" ], "idle": "5m" } } }
```

- `run` is a list of words. errand resolves its placeholders on the machine that runs it:
  - `{python}`: the Python of the env that has the package;
  - `{file:REL}`: a file of the package, where it is installed there;
  - `{tool:NAME}`: a program found there, or fetched;
  - `{port}`: the port, for the keeper.
- `needs` ( `gpus`, `gpu_kind`, `platforms`, `ram` ) and `tools` say what the machine must have. For
  a tool, the description gives:
  - `look`: where it usually is;
  - `version_from`: where to read its version in files ( `Info.plist`, a directory's name, dpkg,
    snap );
  - `min`: the oldest version that will do;
  - `fetch`: a pinned download per platform ( URL, sha256, the program inside ).
- **The version** of a service is 8 hex digits of a sha256 over its protocol and the files
  `version_of` lists. The producer computes it when it writes a directory, and errand computes the
  same on each machine by reading those files. errand never imports the package. Any edit to one
  of those files gives a new version: a machine that authorized the old one shows the service as
  `wrong version`, and a store written after the edit shows it as `installable` again. A new
  `authorize` line authorizes the new version beside the old one.

### The directory says what it asks for

```json
{ "format": 2, "services": {
    "field": { "version": "3fa29c1e", "protocol": 1, "optional": true,
               "package": { "name": "mytool", "version": "1.4" }, "described": { …a copy of the package's entry… } } } }
```

The directory holds names ( `field#3fa29c1e` ), where the code is ( `"source": { "git", "rev",
"dir", "root", "dirty" }`: the producer's repository and commit, `dirty` when its files differ from
that commit ), and a copy of the description, which serves only to propose an installation on a
machine that lacks the code. It never runs anything, and the
directory carries no code.

### Opening it

A run that writes such a directory names it ( `errand.view( dir, open = errand.Static( "index.html" ) )` ), and
`errand run` prints its address beside the run's: the token is in the path, so it opens from the terminal as it is.
A directory already written opens with

```bash
errand open path/to/scene.sviz
```

which serves it beside `errand web` ( the host agent started if need be ) and opens it in the browser. Opened as a
file, its page cannot read the files beside it: it shows that line, with its path.

### The page reaches them through its own address

| request | answer |
|---|---|
| `GET .errand/services` | per service: `version`, `on` ( the machine chosen ), `state`, `why`, `store`, `found`, `lines` |
| `GET .errand/machines` | the machines offered, here first, then the run's, then errand's other machines |
| `GET .errand/machines/<M>[?refresh=1]` | what M's agent found, per service ( it reads files and runs nothing ) |
| `POST .errand/services/<name>` `{"on": M}` | the machine chosen for that service, kept per view |
| `POST .errand/copy` `{"on": M}` | the view's files copied into errand's cache on M ( data only ), kept up to date |
| `POST .errand/machines/<M>/paths` `{"tool", "path"}` | a place for M's agent to look for a tool |
| `.errand/svc/<name>/<path>` | relayed to `name#version` on the machine chosen ( any method, a body, a WebSocket ), started there first if needed |

The states are:

- `running` or `ready`: authorized to pages at this version;
- `installable`: the lines below make it ready;
- `unknown`: a version could not be read from files;
- `unavailable`: greyed, with the reason;
- `wrong version` or `unverified`: see below;
- `unreachable`: the machine's agent did not answer.

The relayed request carries **`X-Errand-Root`**: the directory as the service's machine sees it
( the original, a shared file system, or errand's copy ), percent-encoded.

**Only what both sides name runs.** A page reaches a service only if its `services.json` lists
`name#version` *and* the machine authorizes exactly that to pages. Nothing a page sends says a command: the page gives the
portable `name#version` of its store, and the authorization leads from it to a definition with that machine's paths.

### The lines to type

For a service that is not ready on a machine, errand composes the lines from what that machine has,
and the page shows them. Nothing is run until a person types them:

```text
$ errand serve look --on lmo runs/foam.sviz
lmo  linux-x86_64  RTX 2080 Ti   the store: original /home/hugo/runs/foam.sviz
  blender#3fa29c1e         installable
      · /usr/bin/blender · 4.1.1 ( dpkg ( package blender ) ) — this service needs 5.0 or later
      · sdot 2026.11.3 in /home/hugo/miniforge3/envs/lmo-jax — provides blender#3fa29c1e, the version this store asks for
      # blender 5.2.2 from download.blender.org into errand's cache on lmo ( checked against its sha256 8409…a168 ); nothing in it is run
      errand serve fetch --on lmo https://download.blender.org/…/blender-5.2.2-linux-x64.tar.xz#sha256=8409…
      # the service blender#3fa29c1e, as sdot 2026.11.3 in lmo-jax describes it, GPU 1, stopped after 10m unused
      errand serve authorize --to pages --on lmo --gpus 1 --idle 10m blender#3fa29c1e -- /home/hugo/.cache/errand/tools/8409…/unpacked/… -b …
```

- `errand serve fetch --on M URL#sha256=H`: a file, checked against its hash and kept by its
  contents.
- `errand serve fetch --on M --for NAME#V git+URL@COMMIT`: the package from its repository, into
  that service's own env ( `pip:PKG==V` from an index works the same way ).
- `errand serve fetch --on M git:URL@COMMIT`: only the files of the repository at that commit, for a
  service that needs no Python env of its own ( Blender runs `blender.py` with its own Python ).
- A source outside the list is refused unless you repeat it with `--source URL`. The list
  holds PyPI, conda-forge and download.blender.org by default, and what you add: `errand serve source URL` ( `--off` ), in
  `~/.config/errand/config.json`. The repository of a package already installed here also counts as known.
- A directory written from changes that were never committed has no commit that holds its
  version. The page says so, and the lines then use a checkout of the machine ( `CHECKOUT` ).
- `errand serve authorize --to pages --on M NAME#VERSION -- WORDS` authorizes the service to pages as typed, in
  `authorized-services.json` on M. It records the package that provides that version, and that
  package's files are checked again before every start. If they changed, the state is
  `wrong version` and the service does not start. With no package behind it, the authorization is
  `unverified`. Without `-- WORDS`, the command is composed from the package there, shown, and
  asked. Authorized again, `NAME#VERSION` leads to the new definition alone. Two versions are two servers, whatever runs them.
- `errand serve probe --on M -- PROG --version` runs a program once to read a version that no file
  says. Nothing else runs a program to look at it.

`errand serve` lists every authorization, `authorized` when its server does not run; a page's request that was refused is in
`errand serve authorized` and in the page, where one click ( after the whole command is shown ) authorizes it.

**Without errand** ( the directory under `python -m http.server`, or sent to someone ):
`.errand/services` answers 404, and the page works on its files alone. By convention, a page then
takes a service's address from its URL ( `?svc.field=http://127.0.0.1:9000` ), and a service
started by hand is told its directory with `--root DIR`, since no relay adds `X-Errand-Root`. Such
a service answers CORS for `127.0.0.1` and `localhost`, because the page is on another port.

- **The queue is per user and per machine.** A server started under another account ( a systemd
  service ) is not seen, and its share of the card is not counted. Nor is a run started with
  `--no-queue`: it takes the card without asking, and a service that loads a model next to it may
  find the card full.
- **The server's side of a port is open to every account of the machine**, and so is the announced
  side outside Linux ( see [Only your connections](#only-your-connections) ).
- **From another machine**: `--on` ( above ), or `errand serve connect` behind an ssh key.

### Developing a service

While working on a service, a new version at each edit is in the way. On this file system, say that a
repository is represented by the checkout you work on:

```bash
errand serve dev github.com/sdot-team/sdot ~/Projects/sdot-ffi --env prj-jax      # --off: no more
```

A page whose store asks for a service of that repository then runs it from the checkout, whatever its
version: as the checkout describes it ( its own `errand-services.json` ), a Python service in the env
named, under the name `NAME#dev`. An edit of the files its version is computed over is seen at the next
connection: the service is authorized again and restarted. A page chooses, per service, between the
checkout ( by default, when there is a usable one ) and the store's version.

A copy of the project on another machine ( the copy errand keeps for the runs there ) carries it: the
checkout inside the copy is a dev source on that machine too, with `"envs": { "lmo": "lmo-jax" }` in `config.json` for the
env there. It is only as fresh as the last push. The storage tab lists the dev sources under their disk,
and `d` turns one off ( or on again ): a line in that disk's configuration, never in a store.

### Versions pile up, and go

Each change of a service's files is a new version ( `blender#63ecc287` ), with its declaration, its env and
the code fetched for it. errand keeps track:

```bash
errand serve list --versions          # each version: its size, when it was used, the stores that ask for it
errand serve prune --dry-run          # what the purge would remove
errand serve pin blender#63ecc287     # kept whatever happens ( unpin to undo )
```

A version no store present here asks for, not run for 14 days and not pinned, goes at the host agent's
daily purge, with its env; the code fetched for it goes with the last version that needed it.
`errand serve prune --unused DAYS` does it now. The storage tab of `errand web` lists them under the
disk of errand's directory, `d` removes one.

For a service with no Python of its own ( Blender's ), only what it needs is fetched: its directory and
the files its version is computed over ( `version_of` ), a few dozen KB at the store's commit instead of
the whole repository.

## Next

[Sharing a machine](./machine) · [Agents](./agents) · [What is running](./status)
