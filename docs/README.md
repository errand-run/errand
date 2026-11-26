# The errand website

[VitePress](https://vitepress.dev). Everything web lives under this directory.

```bash
cd docs
npm install
npm run dev        # http://localhost:5173/errand/
npm run build      # -> .vitepress/dist
npm run preview    # serve what was built
```

## The layout

```
index.md                 the home page: the tagline, by hand vs with errand, what you stop doing
guide/                   the documentation: Start, Places, Running, Nothing left behind,
                         Test suites and entries, Project (the sidebar in config.mts is the order)
tutorials/               three walkthroughs, in order
reference/               the command line, the Python API, tag expressions
.vitepress/config.mts    nav, sidebar, base
.vitepress/theme/        the CSS that makes it errand's, and `Term.vue` + `scenes.js`: the terminal
                         recordings (`<Term scene="tui" />`), drawn by hand, cell by cell
```

`base` is `/errand/`, for GitHub Pages under `github.com/errand-run/errand`. Change it in
`config.mts` if the site moves.

## Keeping it true

These pages are the source of record for what errand does; the project's README is a short summary of them. When
a behaviour changes, the page that describes it is part of the change — the build fails on a dead
internal link, which catches a page that was removed but not the one that went quietly out of date.

Pages that make a claim about a flag or an argument should be checked against `cmd/errand/help.go`
(every verb and flag), `internal/layer/` and `lib/errand/`, which are where the answers actually live. A page that moves leaves a stub at its old path (see `guide/remote.md`).

## Publishing

```bash
make site-publish  # from the root of the project
```

It builds the site and sends it to the branch `gh-pages`, which holds that one commit: each publication replaces the
previous one. What is built is `docs/` as it is on disk, committed or not. In the repository's settings, the source of
Pages has to be "Deploy from a branch", `gh-pages`, `/ (root)`.
