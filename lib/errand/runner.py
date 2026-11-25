"""What runs a target, as a project declares it ( ADR 0036 ): what each runner takes, how the entries of its files are
listed, how a set of them is run, how its report is read. It is data: errand's program reads it and does it.

    errand.runners[ "bench" ] = errand.Pytest( dirs = [ "bench" ] )       # one more, which `-r bench` picks
    errand.runners.replace( errand.Catch2( dir = "cpp" ) )                # in place of the built-in one of its kind
    del errand.runners[ "cargo" ]

In a command's words, `{python}` and the like are tools named ( the place's ); `{file}`, `{files}`, `{entries}`, `{args}`,
`{report}`, `{build_dir}`, `{stem}`, `{dir}` are filled where it runs. `zones`: where its tool writes other than the run's
output directory ( ADR 0006 ): path -> "back", "build" or "cache".
"""
from __future__ import annotations

import shlex

# How well a runner fits a file: the highest wins, a tie at the top is an error.
LANGUAGE, FRAMEWORK, ERRAND = 2, 3, 4
SOURCES = ( ".cpp", ".cc", ".cxx", ".c++" )
# A file declares errand's own entries when it IMPORTS errand -- alone, or among others --, not when it merely says the word.
IMPORTS = r"(?m)^[ \t]*(from[ \t]+errand(\.[\w.]+)?[ \t]+import|import[ \t]+([\w.]+([ \t]+as[ \t]+\w+)?[ \t]*,[ \t]*)*errand\b)"


KINDS = ( "back", "build", "cache" )


def check_zones( zones, who ):
    if not isinstance( zones, dict ):
        raise TypeError( f"{who}: a dict, path -> kind ( {' or '.join( KINDS )} ), not {type( zones ).__name__}" )
    for path, kind in zones.items():
        if kind not in KINDS:
            raise ValueError( f"{who}: {path!r} is {kind!r}; a zone is {' or '.join( KINDS )}" )
        if not isinstance( path, str ) or not path.strip( "/" ) or ".." in path.split( "/" ):
            raise ValueError( f"{who}: {path!r} is not a path in the project" )


def _lines( v ) -> list:
    return [ v ] if isinstance( v, str ) else [ str( x ) for x in v or ( ) ]


class Runner:
    """What every runner says. `name` is the key it is stored under."""

    name    = "runner"
    level   = 0
    entries = True          # it answers `x::y`; False: a whole file only

    def __init__( self, zones = None ):
        self.zones = dict( zones or { } )

    def data( self ) -> dict:
        return { "level": self.level, "entries": self.entries, "takes": { }, "zones": self.zones }

    def __repr__( self ):
        return f"<{type( self ).__name__} runner>"


class Errand( Runner ):
    """Files that declare entries with errand's own guard ( `if test( … ):` ), held by `errand.held`."""

    name, level = "errand", ERRAND

    def data( self ) -> dict:
        held = [ "{python}", "-m", "errand.held" ]
        return { **super().data(), "python": True,       # ( errand's own Python: the binding is on its path )
                 "takes": { "suffixes": [ ".py" ], "not_names": [ "errand-*.py" ], "holds": IMPORTS },
                 "list": { "words": [ *held, "list", "{tags}", "{files}" ],
                           "line": r"^(?P<file>[^:\t]+\.py)::(?P<name>[^\t]+)(?:\t(?P<params>--[^\t]*))?(?:\t#.*)?$" },
                 "run": { "words": [ *held, "run", "--report", "{report}", "{tags}", "{entries}", "--", "{args}" ],
                          "entry": "{file}::{name}", "whole": "{file}", "tags": [ "--tags" ], "params": True },
                 "report": { "form": "entries", "file": "entries.jsonl", "nothing": 5 } }


class Pytest( Runner ):
    """A pytest suite: `test_*.py` and `*_test.py`, under `dirs` when they are given. What is in them is pytest's business:
    `x.py::test_y` is handed to it as it is, a pattern is matched against what pytest itself collects, and marks are its own
    ( `-m "not slow"`, after the target ). `python`: the interpreter, when it is not the place's. `spec`: what is installed
    when pytest is missing, unless `install` is False."""

    name, level = "pytest", FRAMEWORK

    def __init__( self, dirs = None, patterns = ( "test_*.py", "*_test.py" ), args = ( ), python = None, install = "auto",
                  spec = "pytest", zones = None ):
        super().__init__( zones )
        self.dirs, self.patterns, self.args = dirs, patterns, [ str( a ) for a in args ]
        self.python, self.install, self.spec = python, install, spec

    def data( self ) -> dict:
        pytest = [ str( self.python or "{python}" ), "-m", "pytest" ]
        return { **super().data(),
                 "takes": { "suffixes": [ ".py" ], "names": list( self.patterns ), "dirs": None if self.dirs is None else _lines( self.dirs ) },
                 # every file of a command in one process: what they import is paid once
                 "list": { "words": [ *pytest, "--collect-only", "-q", *self.args, "{files}" ], "line": r"^(?P<file>[^\s:]+\.py)::(?P<name>\S.*)$" },
                 "run": { "words": [ *pytest, "-q", "--junitxml={report}", *self.args, "{entries}", "{args}" ],
                          "entry": "{file}::{name}", "whole": "{file}" },
                 "report": { "form": "junit", "file": "pytest.xml", "nothing": 5 },      # ( 5: it collected nothing )
                 "requires": { "python": [ { "module": "pytest", "spec": self.spec } ], "install": self.install },
                 "args": self.args }


class Catch2( Runner ):
    """A Catch2 suite: a source is one run of its binary. `x.cpp::add*`, `x.cpp::[math]~[slow]` are Catch2 test specs, handed
    to it as they are written. `build`: the lines that build it, where it runs; `binary`: where one lands
    ( `"{build_dir}/{stem}"` ), beside its source by default."""

    name, level = "catch2", FRAMEWORK

    def __init__( self, dir = None, pattern = "test_*.cpp", build = None, binary = None, args = ( ), zones = None ):
        super().__init__( zones )
        self.dir, self.pattern, self.build, self.binary, self.args = dir, pattern, build, binary, [ str( a ) for a in args ]

    def data( self ) -> dict:
        binary = str( self.binary or "{beside}/{stem}" )
        if self.dir is not None:
            binary = binary.replace( "{dir}", str( self.dir ) )
        return { **super().data(),
                 "takes": { "suffixes": list( SOURCES ), "names": [ self.pattern ], "dirs": None if self.dir is None else [ str( self.dir ) ],
                            "holds": "(?i)catch" },
                 "build": _lines( self.build ), "binary": binary,
                 "missing": "no binary at {binary}: tell Catch2 how to build it ( build = \"make -C cpp\" ) or where it "
                            "lands ( binary = \"{build_dir}/{stem}\" )",
                 "list": { "words": [ "{binary}", "--list-tests", "--verbosity", "quiet" ], "line": r"^(?P<name>\S.*)$", "each": True },
                 "run": { "words": [ "{binary}", "--reporter", "xml::out={report}", *self.args, "{entries}", "{args}" ],
                          "entry": "{name}", "whole": "", "native": True, "join": ",", "escape": "\\,[]" },
                 "report": { "form": "catch2", "file": "catch2.xml" } }


class Cargo( Runner ):
    """A Rust crate: one run per test target -- its source root ( `src/lib.rs`, `tests/foo.rs` ), under the nearest
    `Cargo.toml`. The `::name` part is the filter `cargo test` takes. Compiled where it runs, into this environment's own
    target directory ( None: cargo's )."""

    name, level = "cargo", FRAMEWORK

    def __init__( self, manifest = None, args = ( ), release = False, target_dir = "{build_dir}/target", zones = None ):
        super().__init__( zones )
        self.manifest, self.args, self.release, self.target_dir = manifest, [ str( a ) for a in args ], release, target_dir

    def data( self ) -> dict:
        # which sources are a target's root is cargo's own layout: no cargo is asked to know it
        manifest = str( self.manifest or "{near}" )
        env  = [ ] if self.target_dir is None else [ "env", f"CARGO_TARGET_DIR={self.target_dir}" ]
        test = [ *env, "{cargo}", "test", "--manifest-path", manifest ]
        how  = [ *[ "--release" ] * bool( self.release ), *self.args ]
        return { **super().data(),
                 "takes": { "suffixes": [ ".rs" ], "near": "Cargo.toml", "near_name": r'(?m)^\s*name\s*=\s*"([^"]+)"',
                            "paths": [ { "path": r"src/lib\.rs", "words": [ "--lib" ] },
                                       { "path": r"src/main\.rs", "words": [ "--bin", "{near_name}" ] },
                                       { "path": r"src/bin/(?P<target>[^/]+?)(/main)?\.rs", "words": [ "--bin", "{target}" ] },
                                       { "path": r"tests/(?P<target>[^/]+?)(/main)?\.rs", "words": [ "--test", "{target}" ] } ] },
                 "build": [ " ".join( map( shlex.quote, [ *env, "{cargo}", "test", "--no-run", "--manifest-path", manifest, *how ] ) ) ],
                 "list": { "words": [ *test, "{variant}", *how, "--", "--list" ], "line": r"^(?P<name>\S+): test$", "each": True },
                 # a name typed is cargo's filter; the names of a listing are given `--exact`
                 "run": { "words": [ *test, "{variant}", *how, "--", "{exact}", "{entries}", "{args}" ],
                          "entry": "{name}", "whole": "", "exact": [ "--exact" ] },
                 "report": { "form": "cargo" } }


class Python( Runner ):
    """A `.py`, run by the Python of the place."""

    name, entries = "python", False

    def data( self ) -> dict:
        return { **super().data(), "takes": { "suffixes": [ ".py" ] }, "run": { "words": [ "{python}", "{file}", "{args}" ] },
                 "report": { "form": "exit" } }


class Executable( Runner ):
    """Anything else, run as it is: a file of the project, or a command of the place."""

    name, entries = "executable", False

    def data( self ) -> dict:
        return { **super().data(), "run": { "words": [ "{file}", "{args}" ] }, "report": { "form": "exit" } }


class Runners( dict ):
    """`errand.runners`: name -> runner."""

    def __setitem__( self, name, runner ):
        if not isinstance( runner, Runner ):
            raise TypeError( f"runners[ {name!r} ] must be a runner, not {type( runner ).__name__}" )
        check_zones( runner.zones, f"runners[ {name!r} ].zones" )
        runner.name = name
        super().__setitem__( name, runner )

    def update( self, other = ( ), **kw ):
        for k, v in dict( other, **kw ).items():
            self[ k ] = v

    def replace( self, runner ):
        """-> runner, under the name of the kind it replaces."""
        kind  = type( runner )
        names = [ n for n, r in self.items() if type( r ) is kind ]
        for n in names:
            del self[ n ]
        self[ names[ 0 ] if names else kind.name ] = runner
        return runner


BUILTIN = ( Errand, Pytest, Catch2, Cargo, Python, Executable )
runners = Runners()


def reset( ):
    runners.clear()
    for kind in BUILTIN:
        runners[ kind.name ] = kind()


reset()
