# Writing a runner

A runner is data ( [ADR 0036](https://github.com/errand-run/errand/blob/main/docs/adr/0036-entries-are-listed-a-run-carries-several.md) ):
which files it takes, how what they hold is listed, how a set of it is run, how its report is read. errand does the rest. A tool that is
not one errand comes with is described by a `data()`:

```python
import errand

class Tap( errand.Runner ):
    """programs that say what they did as JUnit"""
    name, level = "mine", 3

    def data( self ):
        return { **super().data(),
                 "takes":  { "suffixes": [ ".mine" ], "names": [ "test_*.mine" ] },
                 "list":   { "words": [ "mine", "--list", "{files}" ], "line": r"^(?P<file>[^:]+)::(?P<name>.+)$" },
                 "run":    { "words": [ "mine", "--junit", "{report}", "{entries}", "{args}" ],
                             "entry": "{file}::{name}", "whole": "{file}" },
                 "report": { "form": "junit", "file": "mine.xml" } }

errand.runners[ "mine" ] = Tap()
```

`errand.runners[ "mine" ]` adds it, under which name `-r mine` picks it ( see [the list of runners](./#the-list-of-runners) ). Nothing else of
the class is read: a method that does something is behaviour, and `errand` refuses the runner, saying which method.

| | |
|---|---|
| `level` | how well it fits a file it takes: the highest wins, a tie is an error. 2 a language, 3 a framework, 4 errand's own entries |
| `takes` | `suffixes`, `names` ( patterns ), `not_names`, `dirs`, `holds` ( a regular expression the file matches ), `near` ( a file above it ) |
| `build` | the lines that build it, once, where it runs |
| `binary` | what is run, when it is built: `"{build_dir}/{stem}"` |
| `list` | `words`, and `line`: what a listed entry looks like, with the groups `file`, `name`. `each`: one listing per file |
| `run` | `words`; `entry`, how one entry is written there; `whole`, how a whole file is |
| `report` | `form`: `junit`, `catch2`, `cargo`, `entries`, `exit`; `file`: what it is called in the run's directory |
| `requires` | `{ "python": [ { "module": "pytest", "spec": "pytest" } ] }`: what its tool needs, installed when it is missing |
| `zones` | where its tool writes other than the run's directory: `{ "/htmlcov": "back" }` |

In words, `{python}`, `{cargo}`, `{cmake}` are tools named, the environment's or the place's; `{file}`, `{files}`, `{entries}`, `{args}`, `{report}`,
`{build_dir}`, `{stem}`, `{dir}`, `{errand_include}` are filled where it runs.

## Still to ship

GoogleTest, doctest, ctest, JavaScript.
