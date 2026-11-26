# errand: compiled and installed here, tested, published. `make` says what there is.
#
# Nothing is compiled but when it is asked: modifying a source changes nothing to the `errand` that is typed until `make install`.

PY ?= python3

.PHONY: help install install-in lib uninstall build wheels test check fmt next rehearse release prerelease site site-build site-publish clean

help:
	@echo "errand $$( $(PY) lib/version.py )"
	@echo
	@echo "  make install      compiled, and installed as a tool ( uv ): the errand that is typed"
	@echo "  make install-in PY=…/python     compiled, and installed in that environment ( pip )"
	@echo "  make lib PY=…/python            errand-lib in that environment, as it is being edited"
	@echo "  make uninstall    the tool, removed"
	@echo "  make build        dist/errand, and the programs of the other systems beside; nothing installed"
	@echo "  make wheels       the wheels of errand-run for every system, in dist/"
	@echo
	@echo "  make test         every test              ( make test ARGS='-run Queue ./internal/queue' )"
	@echo "  make check        formatted, and vetted   ( make fmt formats )"
	@echo
	@echo "  make next         the version a release would be"
	@echo "  make rehearse     what can be done of a release here: tests, packages, installation; nothing is sent"
	@echo "  make release      a tag on main: GitHub tests it on every system and, if all passes, publishes it"
	@echo "  make prerelease   the same, as a candidate ( 2026.11.25.1rc1: pip install --pre )"
	@echo
	@echo "  make site         the website, live        ( make site-build: into docs/.vitepress/dist )"
	@echo "  make site-publish the website, built and sent to GitHub Pages ( the branch gh-pages )"
	@echo "  make clean        dist/, and what Python and the site leave"

install:
	uv tool install --force --reinstall .

install-in:
	$(PY) -m pip install --force-reinstall --no-deps .

lib:
	$(PY) -m pip install -e lib

uninstall:
	uv tool uninstall errand-run

build:
	$(PY) packaging/backend.py build

wheels:
	$(PY) packaging/backend.py wheels

ARGS ?= ./...
test:
	go test $(ARGS)

check:
	@test -z "$$( gofmt -l cmd internal lib )" || { gofmt -l cmd internal lib; exit 1; }
	go vet ./...

fmt:
	gofmt -w cmd internal lib

next rehearse release prerelease:
	@$(PY) packaging/release.py $@

docs/node_modules: docs/package.json
	cd docs && npm install
	@touch $@

site: docs/node_modules
	cd docs && npm run dev

site-build: docs/node_modules
	cd docs && npm run build

# What is built, as the only commit of gh-pages, replaced each time: no history of generated files. It is made with an index of its own, so
# that neither the working tree, the index nor the branches here are touched. Built from the files as they are: the message says `+changes`
# if those of docs/ are not those of the commit.
PAGES_REMOTE ?= origin
PAGES_BRANCH ?= gh-pages
SITE_DIST    := docs/.vitepress/dist
PAGES_INDEX  := $(CURDIR)/docs/.vitepress/cache/pages.index

site-publish: site-build
	@set -e; \
	  touch $(SITE_DIST)/.nojekyll; \
	  mkdir -p $(dir $(PAGES_INDEX)); \
	  export GIT_INDEX_FILE='$(PAGES_INDEX)'; \
	  git read-tree --empty; \
	  git --work-tree=$(SITE_DIST) add --all --force; \
	  tree=$$( git write-tree ); \
	  unset GIT_INDEX_FILE; \
	  from=$$( git rev-parse --short HEAD ); \
	  test -z "$$( git status --porcelain -- docs )" || from="$$from+changes"; \
	  commit=$$( git commit-tree $$tree -m "site, built from $$from" ); \
	  git push --force $(PAGES_REMOTE) $$commit:refs/heads/$(PAGES_BRANCH); \
	  echo "site, built from $$from: sent to $(PAGES_REMOTE), branch $(PAGES_BRANCH)"

clean:
	rm -rf dist docs/.vitepress/dist docs/.vitepress/cache
	find . -name __pycache__ -type d -prune -exec rm -rf {} +
