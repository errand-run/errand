"""Declaring work: `entry`, and the four names it goes by ( `test`, `bench`, `track`, `experiment` ).

    if p := test( "adds", n = Param( 3 ) ):
        assert add( p.n, 1 ) == 4

The guard works in two phases. Collecting, it registers the entry and is falsy: the body is skipped, and the file is read
for what it declares. Running, it is the `Args` of the one entry being run, and falsy for every other guard of the file.

An entry is identified by its CALL SITE -- module and line, and its rank among the entries that line declares ( a loop ) --,
not by its name. What runs a file imports it again for each entry: each body runs on its own ( `held.py` ).
"""
from __future__ import annotations

import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

# What an entry can say of how it runs: the body takes that many processes. What a run asks of the machine otherwise
# belongs to the command ( ADR 0011 ).
RESOURCES = ( "ranks", )


def _truth( text ) -> bool:
    said = str( text ).strip().lower()
    if said not in ( "true", "yes", "1", "on", "false", "no", "0", "off" ):
        raise ValueError( text )
    return said in ( "true", "yes", "1", "on" )


class Param:
    """A typed flag, its type taken from the default: Param( 1000 ) is an int."""
    __slots__ = ( "default", "help", "choices" )

    def __init__( self, default: Any, *, help: str = "", choices: list | None = None ):
        self.default, self.help, self.choices = default, help, choices

    @property
    def ptype( self ):
        return str if self.default is None else type( self.default )

    def read( self, text ):
        return _truth( text ) if self.ptype is bool else self.ptype( text )

    def __repr__( self ):
        return f"Param( {self.default!r} )"


class Args:
    """What the running entry is handed: its parameters, `results` ( numbers to keep ), `out_dir`. Always true."""

    results: dict
    out_dir: Path

    def __init__( self, **kw ):
        self.__dict__.update( kw )

    def __bool__( self ):
        return True

    def __repr__( self ):
        return "Args(" + ", ".join( f"{k}={v!r}" for k, v in self.__dict__.items() ) + ")"


class Entry:
    def __init__( self, name, tags, params, resources, file, line, module, nth ):
        self.name, self.tags, self.params, self.resources = name, tags, params, resources
        self.file, self.line, self.module, self.nth = Path( file ), line, module, nth

    @property
    def site( self ):
        return ( self.module, self.line, self.nth )

    def __repr__( self ):
        return f"<Entry {self.name!r} {self.file.name}:{self.line}>"


# ── the two phases ───────────────────────────────────────────────────────────

target: Entry | None  = None          # the entry being run; None: collecting
collected: list       = [ ]
results: dict         = { }           # the dict behind the running `Args.results`
_resolved: dict       = { }           # parameter values for the running entry
_calls: dict          = { }           # ( module, line ) -> entries declared there so far, in this pass


def begin_collection( ):
    end_run()
    collected.clear()


def begin_run( entry, resolved, out_dir ):
    global target
    target = entry
    _calls.clear()
    results.clear()
    _resolved.clear()
    _resolved.update( resolved )
    os.environ[ "ERRAND_OUT_DIR" ] = str( out_dir )


def end_run( ):
    global target
    target = None
    _calls.clear()


# ── the declaration ──────────────────────────────────────────────────────────

def _declare( name, tags, kw, frame ):
    if "tags" in kw:
        if tags is not None:
            raise TypeError( f"entry( {name!r} ): tags given twice" )
        tags = kw.pop( "tags" )
    params = { k: v for k, v in kw.items() if isinstance( v, Param ) }
    said   = { k: v for k, v in kw.items() if k not in params }
    for k, v in said.items():
        if k not in RESOURCES:
            raise TypeError( f"entry( {name!r} ): unknown keyword {k!r}. A parameter must be a Param ( {k} = Param( {v!r} ) ); "
                             f"the only other keyword is {', '.join( RESOURCES )}: what a run asks of the machine ( --cpus, "
                             f"--gpus, -x ) is the command's" )
    line, module = frame.f_lineno, frame.f_globals.get( "__name__" )
    nth = _calls.get( ( module, line ), 0 )
    _calls[ ( module, line ) ] = nth + 1
    if target is None:
        tags = [ tags ] if isinstance( tags, str ) else list( tags or ( ) )
        collected.append( Entry( name, tags, params, said, frame.f_code.co_filename, line, module, nth ) )
        return False
    if ( module, line, nth ) != target.site:
        return False
    return Args( results = results, out_dir = out_dir(), **_resolved )


def entry( name, tags = None, /, **kw ):
    """Declare a piece of work, or run it. `tags = [ … ]` is accepted as a keyword too; a parameter is recognized by being a
    `Param`; the only other keyword is `ranks`."""
    return _declare( name, tags, kw, sys._getframe( 1 ) )


# What the work is, said in the file for whoever reads it: the four are the same declaration.

def test( name, tags = None, /, **kw ):
    """It must pass."""
    return _declare( name, tags, kw, sys._getframe( 1 ) )


def bench( name, tags = None, /, **kw ):
    """Numbers to follow. Measured alone when the command says so ( `-x` )."""
    return _declare( name, tags, kw, sys._getframe( 1 ) )


def track( name, tags = None, /, **kw ):
    """Numbers to follow over time."""
    return _declare( name, tags, kw, sys._getframe( 1 ) )


def experiment( name, tags = None, /, **kw ):
    """It must be looked at: `latest/` is the path to open."""
    return _declare( name, tags, kw, sys._getframe( 1 ) )


def resolve_params( params: dict, given: dict ) -> dict:
    """Parameter values for one run: what is given, else the default."""
    out = { }
    for name, p in params.items():
        flag, raw = f"--{name.replace( '_', '-' )}", given.get( name )
        try:
            out[ name ] = p.default if raw is None else p.read( raw )
        except ( TypeError, ValueError ):
            raise ValueError( f"{flag}: cannot read {raw!r} as {p.ptype.__name__}" ) from None
        if p.choices and out[ name ] not in p.choices:
            raise ValueError( f"{flag}: {out[ name ]!r} not in {p.choices}" )
    return out


# ── what a body can ask ──────────────────────────────────────────────────────

def out_dir( ) -> Path:
    """This run's own directory ( ./out outside errand, for an ad-hoc run )."""
    return Path( os.environ.get( "ERRAND_OUT_DIR", "out" ) )


def cache_dir( zone: str ) -> Path:
    """Where a cache zone ( ADR 0012 ) is for this run: `zone`, a path from the project root ( `"/data-cache"` ), created
    if missing. The same directory outside errand, run by hand; from here, outside any project."""
    from .alone import root_or_here
    rel = zone.strip( "/" )
    if not rel or ".." in rel.split( "/" ) or any( c in rel for c in "*?[" ):
        raise ValueError( f"cache_dir( {zone!r} ): a path from the project root, as the zone names it" )
    out = root_or_here() / rel
    out.mkdir( parents = True, exist_ok = True )
    return out


def build_dir( name: str = "main" ) -> Path:
    """Where what the project builds goes ( ADR 0039 ): a directory of the project, for this environment and this `name`, created if
    missing -- in errand's caches, found again at the next run, by hand as well ( `errand build-dir NAME` ), and tidied."""
    from .alone import root_or_here
    builds, main = os.environ.get( "ERRAND_BUILDS" ), os.environ.get( "ERRAND_BUILD_DIR" )
    try:
        got = _errand( "build-dir", name )
        out = got.stdout.strip() if got.returncode == 0 else ""
    except OSError:                            # no program here: errand's Python alone, or a run in a place without it
        out = ""
    if not os.path.isabs( out ) or "\n" in out:        # ( nothing, or another program of that name )
        called = re.sub( r"[^A-Za-z0-9._-]", "_", name )
        called = called if called.strip( "." ) else "_" * len( called )
        if builds:
            out = main if name == "main" and main else os.path.join( builds, called )
        else:                                  # by hand, with no errand at all: where `errand build-dir` would put it with no environment
            out = os.path.join( os.environ.get( "ERRAND_OUTPUTS_DIR" ) or root_or_here() / "errand-outputs", "caches", "build", "local", called )
    Path( out ).mkdir( parents = True, exist_ok = True )
    return Path( out )


def library_cache( name: str ) -> Path:
    """Where the library `name` keeps what it can make again ( ADR 0019 ): `$ERRAND_CACHES/<name>` in a run -- the caches of the place
    it runs on, which errand tidies --, the user's own cache otherwise ( `~/.cache/<name>`, `~/Library/Caches/<name>`,
    `%LOCALAPPDATA%` ), else a temporary directory of this user's alone. Created, and tagged as a cache."""
    from .alone import library_cache as found
    return found( name )


def _errand( *words ):
    """errand, where this runs ( `$ERRAND`, ADR 0032 ), else the one that is installed"""
    from .launcher import program
    return subprocess.run( [ os.environ.get( "ERRAND" ) or program(), *words ], stdout = subprocess.PIPE, text = True )


def download( url: str, sha256: str | None = None, name: str | None = None, *, root = None ) -> Path:
    """The path of the file at `url`, downloaded into the project's cache if it is not there yet ( ADR 0013 ). `sha256` is
    checked when given, never required. With no way to the URL, a run asks whoever has one, and waits. Without errand's program,
    fetched by this Python, to the same place."""
    from .alone import root_or_here
    root = root or root_or_here()
    words = [ "download", str( url ), "--root", str( root ) ]
    words += [ "--sha256", sha256 ] if sha256 else [ ]
    words += [ "--name", name ] if name else [ ]
    from .steps import flush
    flush()
    try:
        done = _errand( *words )
    except OSError:                            # no program here
        from .alone import download as fetched
        return fetched( str( url ), sha256, name, root )
    if done.returncode:
        raise RuntimeError( f"{url}: errand download ended with {done.returncode}" )
    return Path( done.stdout.strip().splitlines()[ -1 ] )


def require( gpus: int | None = None, gpu_ram: str | int | None = None, ram: str | int | None = None,
             cpus: int | None = None, exclusive: bool = False, extra: bool = False, ranks: int | None = None ) -> list:
    """Ask the machine for more than the run was started with, and wait until it is given ( ADR 0014 ). Call it early,
    before what it protects is allocated: for Jax, before `import jax`.

        errand.require( gpus = 1, gpu_ram = "75%" )

    `gpu_ram` is what the run takes on each of its cards: a size ( `"40G"` ) or a share ( `"75%"` ); `gpus` without it takes
    cards whole. It asks for that much in all; with `extra`, that much more than the run was started with. It never shrinks
    the claim. Once granted, it sets `CUDA_VISIBLE_DEVICES` ( and the HIP / ROCR ones ) in this process, and returns the
    cards. Outside errand it does nothing and returns `[ ]`.

    What cannot be given where the run is -- more ranks than it has, what an allocation does not have, two runs that wait
    for each other -- stops it, to be run again with what it asked ( ADR 0036 ): under pytest, as a skip of pytest's; in an
    entry, or anywhere else, the process ends with 75."""
    if not os.environ.get( "ERRAND" ) or not os.environ.get( "ERRAND_ASKS" ):
        return [ ]
    asked = { "gpus": gpus, "gpu_ram": gpu_ram, "ram": ram, "cpus": cpus, "ranks": ranks }
    asked = { k: v for k, v in asked.items() if v is not None }
    words = [ w for k, v in asked.items() for w in ( f"--{k.replace( '_', '-' )}", str( v ) ) ]
    done = _errand( "require", *words, *[ "-x" ] * bool( exclusive ), *[ "--extra" ] * bool( extra ) )
    if done.returncode == 75:
        said = ", ".join( [ f"{v} ranks" if k == "ranks" else f"{k} = {v}" for k, v in asked.items() ]
                          + [ "the machine to itself" ] * bool( exclusive ) )
        message = f"asks for {said}, which this run does not have: errand runs it again with them"
        if os.environ.get( "PYTEST_CURRENT_TEST" ):
            import pytest
            pytest.skip( message )
        print( f"errand.require: {message}", file = sys.stderr, flush = True )
        raise SystemExit( 75 )
    if done.returncode:
        raise RuntimeError( f"errand require ended with {done.returncode}" )
    cards = [ ]
    for line in done.stdout.splitlines():
        name, _, value = line.partition( "=" )
        if name:
            os.environ[ name ] = value
        if name == "CUDA_VISIBLE_DEVICES":
            cards = [ int( d ) for d in value.split( "," ) if d.strip().isdigit() ]
    return cards


def memo( fn = None, **kw ):
    """`@errand.memo` or `@errand.memo( version = 2 )`: the function's result kept by its arguments, in the project's cache
    ( `errand-cache/memo/` ), computed once across every machine that runs it ( ADR 0015 ). `memo.mode`: None
    ( `ERRAND_MEMO`, else "on" ), "off" ( compute, keep nothing ), "refresh" ( compute, replace )."""
    from .memos import memo as _memo                 # only when a function is decorated
    return _memo( fn, **kw )


memo.mode = None


# ── the tags of the environment ──────────────────────────────────────────────

def _tags( ) -> dict:
    out = { }
    for item in filter( None, os.environ.get( "ERRAND_TAGS", "" ).split( "," ) ):
        k, _, v = item.partition( "=" )
        out[ k ] = v if v else True
    return out


def has_tag( expr: str ) -> bool:
    """Does the selected environment satisfy `expr` ( `cuda & !jax`, `fp=64` )? Outside errand -- a file run by hand --, yes
    to everything: a file that silently disappears is worse than an import that fails loudly."""
    tags = _tags()
    return True if not tags else matches( expr, tags )


def tag( name: str, default: Any = None ):
    """The value of a tag of the selected environment."""
    return _tags().get( name, default )


def matches( expression: str, tags: dict ) -> bool:
    """Does `tags` satisfy `expression`? Literals, `!`, `&`, `|` ( binding in that order ), `=` for a valued tag; no
    parentheses."""
    def literal( text ):
        text = text.strip()
        if text.startswith( "!" ):
            return not literal( text[ 1 : ] )
        name, sep, wanted = ( s.strip() for s in text.partition( "=" ) )
        if not name:
            raise ValueError( f"empty tag in expression: {expression!r}" )
        if name not in tags:
            return False
        have = tags[ name ]
        if not sep:
            return True
        if isinstance( have, bool ):
            return wanted.lower() in ( ( "true", "yes", "1" ) if have else ( "false", "no", "0" ) )
        return str( have ) == wanted
    if not ( expression or "" ).strip():
        return True
    return any( all( literal( lit ) for lit in conj.split( "&" ) if lit.strip() ) for conj in expression.split( "|" ) )
