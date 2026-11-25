"""The entries files hold: each is imported, under a private name, freshly every time."""
from __future__ import annotations

import contextlib
import importlib.util
import sys
import traceback
from pathlib import Path

from . import entries as E

SCAN = "_errand_scan"

# Files that declare work and could not be read, from the last `collect`: [ ( path, the short of it, the whole traceback ) ].
# A list and not an exception: one file that will not import must not cost every other entry. What could not be read is
# reported, and it fails the run.
broken: list = [ ]


def import_file( path: Path, root: Path ):
    """Import `path` under `_errand_scan.<dir>...<stem>`. Its ancestors are namespace packages with their real paths, so that
    a relative import resolves against the file's own directory; only the leaf is executed again.

    -> ( module, name ), or ( None, name ) when the file withdrew itself with `sys.exit`: the way it says it does not apply
    to this run, before importing what would fail."""
    parts = path.resolve().relative_to( root.resolve() ).with_suffix( "" ).parts
    pkg, directory = SCAN, root.resolve()
    for part in parts[ : -1 ]:
        pkg, directory = f"{pkg}.{part}", directory / part
        if pkg not in sys.modules:
            module = importlib.util.module_from_spec( importlib.util.spec_from_loader( pkg, loader = None, is_package = True ) )
            module.__path__ = [ str( directory ) ]
            sys.modules[ pkg ] = module
    name = f"{pkg}.{parts[ -1 ]}"
    spec = importlib.util.spec_from_file_location( name, path )
    module = importlib.util.module_from_spec( spec )
    sys.modules[ name ] = module
    # Work is declared NEXT TO the code it exercises: `test_primes.py` imports `primes` from its own directory, wherever
    # errand was started from. Taken off again straight afterwards: it would decide somebody else's import later on.
    here = str( path.resolve().parent )
    borrowed = here not in sys.path
    if borrowed:
        sys.path.insert( 0, here )
    try:
        spec.loader.exec_module( module )
    except SystemExit:
        if E.target is not None:             # ( the body of the entry that runs: how it ends is its business )
            raise
        sys.modules.pop( name, None )
        return None, name
    finally:
        if borrowed:
            with contextlib.suppress( ValueError ):
                sys.path.remove( here )
    return module, name


def collect( files: list, root: Path ) -> tuple:
    """-> ( the entries of the files, { module name: path } )"""
    E.begin_collection()
    broken.clear()
    modules = { }
    for f in files:
        try:
            module, name = import_file( f, root )
        except KeyboardInterrupt:
            raise
        except BaseException as err:
            broken.append( ( f, f"{type( err ).__name__}: {err}", failure( err ) ) )
            continue
        if module is not None:
            modules[ name ] = f
    return list( E.collected ), modules


# ── a failure, without errand in it ( ADR 0016 ) ─────────────────────────────

_HERE = str( Path( __file__ ).resolve().parent )


def _errands( filename: str ) -> bool:
    return filename.startswith( "<frozen importlib" ) or str( Path( filename ).resolve() ).startswith( _HERE )


def _trim( te, seen: set ):
    if te is None or id( te ) in seen:
        return
    seen.add( id( te ) )
    kept = [ f for f in te.stack if not _errands( f.filename ) ]
    if kept:                                   # none but errand's: errand failed, and says where
        te.stack = traceback.StackSummary.from_list( kept )
    for other in ( te.__cause__, te.__context__, *( getattr( te, "exceptions", None ) or [ ] ) ):
        _trim( other, seen )


def failure( exc: BaseException, color = False ) -> str:
    """`exc` as Python prints it, errand's frames and the import machinery's left out."""
    te = traceback.TracebackException.from_exception( exc )
    _trim( te, set() )
    try:
        return "".join( te.format( colorize = color ) )
    except TypeError:                          # a Python that does not color its tracebacks
        return "".join( te.format() )


def print_failure( exc: BaseException ):
    sys.stdout.flush()
    color = False
    with contextlib.suppress( Exception ):
        import _colorize                       # Python 3.13+: as `print_exc` would decide
        color = _colorize.can_colorize( file = sys.stderr )
    sys.stderr.write( failure( exc, color ) )
    sys.stderr.flush()
