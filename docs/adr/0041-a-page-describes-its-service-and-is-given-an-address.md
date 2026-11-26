---
status: proposed
date: 2026-11-26
amends: 0026, 0027, 0028, 0040
---

# A page describes its service, and is given an address or a refusal

ADR 0027 let only the packages installed on a machine say what a page's service runs. The store held names, and a copy of the description
that never ran. The reason: *a store can carry any command*. Since ADR 0040 ( step 4 ), a page reaches a server only if a person has
authorized it, with the whole command in front of them. Authorization now covers that risk, and the rest of 0027's machinery is no longer
needed for it:

- the entry point `errand.services`, `errand-services.json` read in the envs of each machine, `NAME#VERSION` computed from files by the
  producer and by errand alike;
- a choice of machine kept by errand per view, so that a page cannot direct each request where it wants;
- a services panel in every page that uses one ( about 200 lines of sdot's viewer ): machines diagnosed in parallel, what each agent found,
  lines to copy, a path per tool, a button to copy the store. Each page had to follow errand's changes.

Hugo, 2026-11-26: the page should ask for an address by describing its service, as a program does for Ollama, and be told when it is not
authorized. Settled in a grill the same day ( `grill/page-services-2/decisions.md`, three rounds ).

## Decision

### The page says its service, in full; its hash is its identity

```js
const r = await fetch( ".errand/srv", { method: "POST", body: JSON.stringify( {
  on: machine,                                          // an id from GET .errand/machines; none: the machine that serves the page
  name: "blender", protocol: 1, text: "Pictures of the current view by Blender",
  run: [ "{tool:blender}", "-b", "--factory-startup", "-P", "{file:blender.py}", "--", "--port", "{port}" ],
  files: { git: "https://github.com/sdot-team/sdot", rev: "807e184", dir: "src/sdot/viz/services" },
  tools: { blender: { min: "5.0", look: { … }, version_from: [ … ], fetch: { … } } },
  needs: { gpus: "0-1" }, idle: "10m" } ) } ) ).then( r => r.json() );
// { url: ".errand/srv/9c41e2d0/blender~5c1e09ab/", state: "rests" }
// { error: "not authorized", why: "…", html: "…", authorize: "http://127.0.0.1:20501/authorize", form: { described, on, view } }
```

- The description is **portable**. It names no path of any machine. Its placeholders are 0027's closed set, resolved on the machine that serves
  when a person authorizes it:
  - `{tool:NAME}`: a program found there ( `look`, `PATH`, errand's cache ), its version read from files ( `version_from` ), or fetched ( `fetch` );
  - `{file:REL}`: a file of the code at `source`;
  - `{python}`: the service's own env, made from `source`;
  - `{port}`, `{port:NAME}`.
- Its **hash**, eight hex digits of the normalized description, is computed by errand: the producer no longer computes versions. Another
  `rev` is another service. `on` is not part of it.
- The description may come from the store's `services.json`, or be written in the page: errand does not tell them apart.
- **In development**, `source` says `dev: true` instead of a `rev`, and `version_of` names the files to watch. The hash does not depend on the
  code. The checkout is chosen on the machine when authorizing, among its `dev_sources` ( ADR 0028 ). An edit makes another server behind the same
  address ( ADR 0040: a program that changed ).

### The address names the machine, by an id

`.errand/srv/<MACHINE>/<NAME>~<HASH>/`, relative to the view:

- one origin, one token, no CORS; the relay adds `X-Errand-Root`, the store as that machine sees it ( ADR 0026 );
- `MACHINE` is eight hex digits of the machine's `system.MachineID`, never a name, which can be ambiguous. `GET .errand/machines` gives the ids
  with their names and whether each is this machine, for the page's menu. The page keeps its choice; errand keeps none;
- `~`, not `#`: a browser never sends what follows `#`. `HASH` is the description's, which is also the authorization's;
- the address is valid as long as the authorization holds. **There is no lease**: the relay of errand's page is always listening, `idle` and
  `grace` come from the authorization, and a connection through the relay counts as a use. A request that finds the server stopped starts it.
  Once the authorization is withdrawn, the address answers 403 `not authorized`.

`.errand/svc/<NAME>/`, `POST .errand/services/<NAME>`, `.errand/machines/<M>`, `.errand/machines/<M>/paths` and `.errand/copy` go.

### A refusal says why, and where to set it up

`{ "error": "not authorized" | "no store", "why", "html", "authorize" }`:

- `html` is a fragment, for the page to insert as it is: the state, what the machine has and lacks, and a button *Set it up on lmo…*: a form
  that POSTs the description, the machine and the view to `authorize` ( `target="_blank"` ). It holds no script. It has classes ( `errand-ok`,
  `errand-missing`, `errand-setup` ) and a small style of its own. The page adds a *Retry* button. **Nothing polls.**
- `authorize` is an address of **errand's own page**, not of the view. **Nothing is kept** of a refusal ( amended 2026-11-26, round 4: an hour in
  memory was too much ): the window gets the description in the form, shows it, and Authorize sends it back.
- Every refusal has its button, but a description that cannot be read; one in development without a checkout there is `missing` a checkout,
  which the window declares.

### Authorizing is done in errand's page, as a card payment is

The page's form opens it in a new window. The page cannot read that window, nor click in it:

- it is another origin ( errand's page, not the view's port ); it takes a form only from a view's origin, and its own calls only from its own;
- it needs errand's cookie ( `HttpOnly`, `SameSite=Strict`: sent from a view, which is the same site, not from another one ): another account of
  the machine can do nothing there;
- it is never framed ( `frame-ancestors 'none'`, `X-Frame-Options: DENY` ).

The window shows, for the machine chosen:

1. the description, and what it was resolved to: each tool found with its version, the code at `rev` ( or the checkout, in development ),
   the cards, the store;
2. what is missing, with its source, its size and its sha256: a Blender to fetch, the repository at that commit, an env to build, the view's
   files to copy. A source outside `errand.sources` is red, and needs a box ticked;
3. a field per tool, for a path to look at; a *Probe* button when a version cannot be read from files, with the exact command it runs once;
4. the whole command, with that machine's paths;
5. one **Authorize** button: it fetches what is missing, composes, copies the store if needed, and writes the authorization `to: pages` ( the
   portable hash leads to that machine's definition ). The window follows the progress, then closes.

The page's *Retry* then gets its address. From the command line, the same: `errand serve authorize --to pages --on M --spec -`, the
description on standard input, everything shown, a yes asked.

### What else goes

- From ADR 0027:
  - the entry point and the reading of `errand-services.json` by errand ( a producer may keep the file as its own source of the description );
  - `NAME#VERSION` from `version_of`;
  - the diagnosis menu;
  - `errand serve look` and `errand serve install` ( already replaced by `authorize` ).

  `errand serve fetch`, `errand.sources` and the service envs stay; `probe` and the paths per tool move to the authorization window.
- From ADR 0040 step 4: the refusals written to `refused-services.json` and the list "refused lately", for keys too. A refused key leaves a line in
  the agent's journal.
- A store copied to another machine ( 0027 §3 ) is kept up to date as long as the view is open, as before. The copy is now started only by
  *Authorize*, never by a page.

### Without errand

Unchanged ( ADR 0026 ): under any other static server `.errand/srv` is a 404; the page works on its files, and `?svc.NAME=URL` points a service at a
server started by hand with `--root`.

## Considered options

- **The page gives only `NAME#VERSION`, the package on the machine describes** ( 0027 as built ): it took entry points, a version algorithm shared
  with every producer, and a panel in every page. Authorization now does what that was for.
- **The choice of machine kept by errand per view**: it was protecting against a page directing requests. A page reaches only what was authorized
  on a machine, wherever it points.
- **Refusals kept on disk**: nothing reads them again but the window that authorizes, which needs them for minutes.
- **The diagnosis in an iframe of errand's**: nicer, but a framed button can be clicked without the person knowing.
- **Polling until authorized**: a *Retry* button is enough, and costs nothing while nobody looks.
- **A JS module served by errand**: an import fails without errand, which every page would have to handle; one POST does not.

## Costs

- A page carries its description ( or reads it from its store ), with the tools' rules: longer than a name.
- Authorizing another machine than the one that serves the page goes through its agent, and waits for its downloads.
- sdot changes: `declare()` writes the description with `source`; the viewer's panel becomes the menu of machines, the fragment, *Retry*.

## Steps

1. `POST .errand/srv`: the description, its hash, `on` by id; the relay at `.errand/srv/<MACHINE>/<NAME>~<HASH>/`; the refusal with `html` and
   `authorize`; `GET .errand/machines` with ids. The old calls go.
2. The authorization window in errand's page: resolution on the machine chosen, what is missing, paths and probe, *Authorize* ( fetch, compose,
   copy, write ), its progress; `errand serve authorize --to pages --spec -`.
3. Entry points, versions from files, `look`, `install`, the stored refusals go.
4. The guide ( *A page that asks for a service* ), the reference, the skill.
5. sdot, by its own session: the description, the call, the panel reduced.

## Progress

- 2026-11-26, errand steps 1-4 done (uncommitted):
  - `svc/pagedesc.go`: `PageDesc` ( parsed strictly, `on` aside ), its hash and `NAME~HASH`; `LookPage` resolves it on a machine
    ( the code at `rev`, or a `dev_sources` checkout; the service's env; tools found, or to fetch; probes; the view's files ) and says
    what lacks; `AuthorizePage` fetches what lacks, from trusted or accepted sources only, then authorizes `to: pages`.
  - `page/services.go`: `GET .errand/machines` with ids, `POST .errand/srv`, the relay `.errand/srv/<MACHINE>/<NAME>~<HASH>/`, the
    refusal with its read-only fragment; `page/authorize.go` and `page/authorize.html`: the window, on errand's page.
  - The agent's `page_look`, `store_seen` and `authorize` ( a description ); `errand serve authorize --to pages --spec -`.
  - Gone: `asked.go`, the entry points and `errand-services.json` read by errand, versions from files, `serve look`, `dev_ensure`,
    `refused-services.json` and the refused rows of the page.
  - Departures: a service's env is `svc-envs/NAME@HASH`; `source.package` names the Python package's place in the repository
    ( `#subdirectory=` ), `source.pip` and `source.index` stand for a package of an index. A key refused leaves a line in the agent's
    stderr, its journal.
  - Tried in a browser against an isolated errand: the fragment in a page, the window, *Authorize*, *Retry*, the service answering.
    Not tried: another machine ( only the end-to-end test's local relay ), a real Blender fetch, a `{python}` env built from git.
- 2026-11-26, after Hugo tried sdot's viewer ( round 4 and after ):
  - nothing of a refusal is kept: the fragment's form carries the description to the window, which asks errand's token when the browser
    has no cookie yet ( the description kept in that form ), and sends it back to authorize;
  - every refusal has its button but an unreadable description; a checkout is declared in the window;
  - the fragment says the idea ( a sentence, and `short` per thing missing: "sdot's code, to fetch" ), the window the details;
  - `needs.gpus` may be a range, `"0-1"`: resolved on each machine ( a Mac's GPU is no card errand counts ).
  - the window shows one part per need ( the code, its env, each tool, the machine, the view's files ), green or orange, each with what it
    lets a person set ( a tool's path, the checkout's, the command `errand serve dev REPO PATH` written out ) and the description's part, folded;
  - **in development, `{python}` is an env errand builds** ( Hugo: the request says what it needs, errand installs it ): the checkout
    installed in the service's own env ( `pip install -e`, `source.package` its place ), kept and pruned with it. This replaces, for pages,
    the env a dev source named ( ADR 0028 §3 ).
  - **files and env apart** ( round 5, the case of loom ): `source` goes. `files` is a repository the service takes files of
    ( `{file:}` ), at a commit or as a checkout, nothing installed; `python: { install, index }` is what its env installs, in one
    `pip install`: requirements of the index, and repositories ( `git+URL@REV`, or `-e CHECKOUT` in development ), which then
    provide their packages in place of the index's. The page chooses commit or checkout per service and per repository ( each service has its env: revised after Hugo tried it ); `version_of` is in each
    repository's entry, patterns ( `**` ) read again by date and size.
