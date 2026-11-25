"""`errand-*.py`: what a project declares, held as it is read.

Ordinary Python, loaded once BY PATH, under private names: loading one can never be what shadows the package it imports from.
There is no entry point to call and nothing to return: `configure`, `envs` and `runners` hold what they are given.
"""
from __future__ import annotations

import dataclasses
import hashlib
import importlib
import importlib.util
import os
import re
import sys
import types
from dataclasses import dataclass, field
from pathlib import Path

from . import layers as L, local, runner
from .runner import check_zones

CONFIG_GLOB = "errand-*.py"            # every one at the root is read; the root is the nearest directory that has one


def config_files( directory: Path ) -> list:
    """The project files in `directory`, in the order they are read: `errand-vars.py` first, so that the files declaring
    environments can ask it for the hosts and paths of this machine."""
    return sorted( ( p for p in directory.glob( CONFIG_GLOB ) if p.is_file() ), key = lambda p: ( p.name != local.VARS_FILE, p.name ) )


# ── an environment ───────────────────────────────────────────────────────────

NEEDS = ( "cpus", "ram", "gpus", "gpu_ram", "exclusive" )
_SIZE = re.compile( r"\s*\d+(\.\d+)?\s*[KMGT]?B?\s*", re.I )
_SHARE = re.compile( r"\s*\d+(\.\d+)?\s*%\s*" )


def _check_need( name, v ):
    if name not in NEEDS:
        raise ValueError( f"needs[ {name!r} ]: one of {', '.join( NEEDS )}" )
    text = str( v )
    fits = isinstance( v, bool ) if name == "exclusive" else v is None or ( _SIZE.fullmatch( text ) if name.endswith( "ram" ) else
                                                                          re.fullmatch( r"\d+(\.\d+)?", text ) ) \
        or name == "gpu_ram" and _SHARE.fullmatch( text )
    if not fits:
        raise ValueError( f"needs[ {name!r} ] = {v!r}: not a { 'size ( 16G )' if name == 'ram' else 'size or a share ( 40G, 75% )' if name == 'gpu_ram' else 'number' if name != 'exclusive' else 'truth' }" )


class Env:
    """Where work runs: a stack of layers, and the tags saying what it is.

        errand.envs[ "gpu" ] = errand.Env( [ errand.Apptainer( ... ) ], driver = "cuda" )

    The preparation ( setting up, building ) goes through the same layers, unless `prepare` gives it its own: another
    allocation, another machine. Copy what both share:

        base = [ errand.Ssh( host = "login.hpc", root = "/scratch/me/proj" ) ]
        errand.Env( base + [ errand.Slurm( gpus = 1 ) ], prepare = base + [ errand.Slurm( cpus = 16 ) ] )

    `tools` names what the runners start in this environment, where its usual name is wrong: `{ "python": "python3.12" }`.
    `needs` is what every run in it takes of the machine, known before anything starts ( ADR 0014 ): `{ "gpus": 1, "gpu_ram":
    "75%", "ram": "16G" }`; the command line is over it. The name is the key it is stored under."""

    def __init__( self, stack = ( ), prepare = None, tools = None, needs = None, **tags ):
        for k, v in tags.items():
            if isinstance( v, str ) and "," in v:
                raise ValueError( f"tag {k} = {v!r}: a tag value cannot hold a comma, which means several values on the command line" )
        self.name    = ""
        self.stack   = list( stack )
        self.prepare = None if prepare is None else list( prepare )
        self.tags    = dict( tags )
        self.tools   = dict( tools or { } )
        self.needs   = dict( needs or { } )
        for name, v in self.needs.items():
            _check_need( name, v )
        for name, argv in self.tools.items():
            if not isinstance( argv, ( str, list, tuple ) ) or not argv:
                raise ValueError( f"tools[ {name!r} ]: a command, as a string or a list of words" )
        check_stack( self.stack )
        if self.prepare is not None:
            check_stack( self.prepare, " ( in `prepare` )" )
            if L.first( self.stack, L.Windows ) != L.first( self.prepare, L.Windows ):
                raise ValueError( "`prepare` and the run boot different machines: one errand boots is the same for both -- copy the first layer" )

    @property
    def ssh( self ):
        return L.ssh_of( self.stack )

    @property
    def vm( self ):
        return L.vm_of( self.stack )

    @property
    def container( self ):
        return L.container_of( self.stack )

    @property
    def missing( self ):
        """The `Missing` layer that says why this environment cannot be reached from here, or None."""
        return L.missing_of( [ *self.stack, *( self.prepare or ( ) ) ] )

    def __repr__( self ):
        return f"Env( {self.name!r}, {self.stack!r} )"


def check_stack( stack, where = "" ):
    """What a stack must look like: said where it is written, not on the day somebody first runs in it."""
    for i, layer in enumerate( stack ):
        kind = type( layer ).__name__
        if isinstance( layer, L.Ssh ) and i:
            raise ValueError( f"Ssh must be the first layer{where}" )
        if isinstance( layer, ( L.Windows, L.WindowsBox ) ) and i and not ( i == 1 and isinstance( stack[ 0 ], L.Ssh ) ):
            raise ValueError( f"{kind} must be the first layer, or follow its Ssh{where}" )
        if isinstance( layer, L.WindowsBox ) and not i:
            raise ValueError( f"WindowsBox must follow the Ssh that reaches the machine{where}" )
        if isinstance( layer, L.Container ) and not getattr( layer, "image", "x" ):
            raise ValueError( f"{kind}( image = ... ) is empty: name the image" )
        if isinstance( layer, L.Micromamba ) and not layer.name:
            raise ValueError( f"{kind}( name = ... ) is empty: name the environment" )
    if sum( isinstance( l, L.Slurm ) for l in stack ) > 1:
        raise ValueError( f"several schedulers{where}: one allocation per list; to prepare in another one, give it its own "
                          f"layers: Env( [ ... ], prepare = [ ... ] )" )


class Envs( dict ):
    """`errand.envs[ "name" ] = errand.Env( ... )`: the key becomes the environment's name."""

    def __setitem__( self, name, env ):
        if not isinstance( env, Env ):
            raise TypeError( f"envs[ {name!r} ] must be an errand.Env, not {type( env ).__name__}" )
        if "," in name:
            raise ValueError( f"envs[ {name!r} ]: an environment name cannot hold a comma, which means several on the command line" )
        env.name = name
        super().__setitem__( name, env )


# ── a project's settings ─────────────────────────────────────────────────────

@dataclass
class Settings:
    # Where the project's own code is, from its root, put in front of any other checkout of it ( `src` when there is one )
    src    : list = field( default_factory = list )
    # Directories a target never walks into: vendored code, fixtures
    exclude: list = field( default_factory = list )
    # Where what is built goes, one directory per environment under it: two never share a binary. None: in errand's caches for
    # the project ( `errand.build_dir`, ADR 0039 ), which keeps them tidy; a path from the root: the project's to tidy
    build  : str | None = None
    # Everything errand writes for this project ( `runs/`, `builds/`, `state/` ), from its root, or absolute.
    # `$ERRAND_OUTPUTS_DIR` wins; over there, `Ssh( errand_outputs_directory = ... )`.
    outputs_directory: str = "errand-outputs"
    # What errand builds ( `builds/` above ) somewhere else: another disk, or one directory several projects share
    builds_directory : str | None = None
    # Where a run writes in the project's copy over there ( ADR 0006 ). `{ "results": "back" }`: path patterns ( `results`, a
    # name anywhere; `/results`, at the root ), each a kind -- `back`, written there and brought back; `build`, built there;
    # `cache`, added to on both sides, never replaced nor deleted ( ADR 0012 ). A path may name `{env}` or `{build_dir}`.
    zones            : dict = field( default_factory = dict )
    # How `errand run` draws a run's output ( ADR 0016 ): behind a `bar`, behind `spaces`, or `off`; `plain`: no colors
    output           : str = "bar"
    # Each run's page opened in the browser as it starts ( ADR 0017 §5 )
    open             : bool = False


def configure( **kw ):
    """Project-wide settings: the fields of `Settings`."""
    for k, v in kw.items():
        if not hasattr( settings, k ):
            raise TypeError( f"configure(): unknown setting {k!r}; expected one of {', '.join( vars( settings ) )}" )
        if k == "zones":
            check_zones( v, "configure( zones = ... )" )
        if k == "output" and v not in ( "bar", "spaces", "off", "plain" ):
            raise ValueError( f"configure( output = {v!r} ): bar, spaces, off or plain" )
        setattr( settings, k, v )


settings = Settings()
envs     = Envs()
# What declares environments before the project does: functions of the project's root that fill `envs`. An installed package
# declares one under the entry point group `errand.envs`. They run after `errand-vars.py`, and before the project's other
# files, which can then redefine what they declared. One that reads files of its own answers their paths: a document kept of
# the project is made again when one changes. `$ERRAND_PROVIDERS` ( `module:function`, separated by commas ) names more, for a
# package that is on the path without being installed.
providers = [ ]
GROUP     = "errand.envs"
NAMED     = "ERRAND_PROVIDERS"
read_too: list = [ ]           # the files the providers read
provided: set = set()          # id() of the environments a provider declared: never the default


def reset( ):
    for f in dataclasses.fields( Settings ):
        setattr( settings, f.name, getattr( Settings(), f.name ) )
    errand = sys.modules[ "errand" ]
    errand.default_env = errand.default_run = None
    envs.clear()
    provided.clear()
    read_too.clear()
    runner.reset()


def load( root: Path ) -> bool:
    """Read the project's files, if there are any -> were there? Each one's plain values are offered to the files after it,
    and to entries ( `errand.value`, `errand.need` )."""
    root = Path( root )
    reset()
    files = config_files( root )
    local.reset( files, root )
    package, asked = _package( root ), False
    for path in files:
        if not asked and path.name != local.VARS_FILE:
            _provide( root )
            asked = True
        spec = importlib.util.spec_from_file_location( f"{package}._errand_" + re.sub( r"\W", "_", path.stem ), path )
        module = importlib.util.module_from_spec( spec )
        spec.loader.exec_module( module )
        local.offer( module )
    if not asked:
        _provide( root )
    return bool( files )


def _package( root: Path ) -> str:
    """A private package whose directory is the root: `from .helpers import X` in a project file reads `helpers.py` beside it,
    and nothing is put on `sys.path`."""
    name = "_errand_root_" + hashlib.sha1( str( root.resolve() ).encode() ).hexdigest()[ :12 ]
    for key in [ k for k in sys.modules if k == name or k.startswith( name + "." ) ]:
        del sys.modules[ key ]
    package = types.ModuleType( name )
    package.__path__ = [ str( root ) ]
    sys.modules[ name ] = package
    return name


def _provide( root: Path ):
    """The providers, then those installed packages declare. One that fails is said and skipped."""
    def say( text ):
        print( f"errand: {text}", file = sys.stderr )
    found = list( providers )
    for name in filter( None, ( n.strip() for n in os.environ.get( NAMED, "" ).split( "," ) ) ):
        module, _, attr = name.partition( ":" )
        try:
            fn = getattr( importlib.import_module( module ), attr or "provide" )
        except Exception as err:
            say( f"the environments of `{name}` ( ${NAMED} ) are not loaded: {err}" )
            continue
        if fn not in found:
            found.append( fn )
    try:
        from importlib.metadata import entry_points
        installed = entry_points( group = GROUP ) if sys.version_info >= ( 3, 10 ) else entry_points().get( GROUP, ( ) )
    except Exception:
        installed = ( )
    for ep in installed:
        try:
            fn = ep.load()
        except Exception as err:
            say( f"the environments of `{ep.value}` ( entry point {GROUP} ) are not loaded: {err}" )
            continue
        if fn not in found:
            found.append( fn )
    for fn in found:
        before = set( map( id, envs.values() ) )
        try:
            read_too.extend( Path( p ) for p in fn( root ) or ( ) )
        except Exception as err:
            say( f"`{getattr( fn, '__module__', '' )}.{getattr( fn, '__qualname__', fn )}` failed to declare its environments: {err}" )
        provided.update( id( e ) for e in envs.values() if id( e ) not in before )
