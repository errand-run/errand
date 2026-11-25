"""errand's own entries, as a tool like any other ( ADR 0036, step 7 ):

    python -m errand.held list [--tags EXPR] FILE…                       what the files hold, one entry per line
    python -m errand.held run --report R [--tags EXPR] FILE[::NAME]… [-- ARGS…]   run them, in this one process

What orchestrates starts this as it starts pytest: in the environment, with a set of entries. One process carries the
set; a file is imported again for each entry it runs, so that each body runs on its own and each failure lands on its
own entry ( `entries.py` ). What came of each entry is one line of JSON in the report: its name, its status, how long
it took, a word, its numbers ( `p.results` ).

The arguments after `--` are the entries': a declared parameter is read out of them ( `--n=5`, `--n 5` ), the rest is
left in `sys.argv`, as it is written. An entry that says `ranks = N` and is not run on that many asks errand for them
( `require` ), and is run again with them.
"""
from __future__ import annotations

import contextlib
import json
import os
import sys
import time
from pathlib import Path

from . import discovery, entries as E, local


def _slug( name: str ) -> str:
    return "".join( c if c.isalnum() or c in "-_." else "_" for c in name ).strip( "_" ) or "entry"


def _rank( ) -> int:
    for var in ( "PMI_RANK", "OMPI_COMM_WORLD_RANK", "PMIX_RANK", "SLURM_PROCID" ):
        if os.environ.get( var, "" ).isdigit():
            return int( os.environ[ var ] )
    return 0


def _root( ) -> Path:
    return Path( os.environ.get( "ERRAND_ROOT" ) or Path.cwd() ).resolve()


def _read( files, tags_expr ):
    """-> ( the entries of the files, { module: path } ), what could not be read said on stderr"""
    root = _root()
    with contextlib.suppress( Exception ):     # what only this machine can supply ( `errand.value`, `need` ): its files
        from . import config
        config.load( root )
    found, modules = discovery.collect( [ ( root / f ).resolve() for f in files ], root )   # ( its sources: on PYTHONPATH )
    if tags_expr:
        found = [ e for e in found if E.matches( tags_expr, { t: True for t in e.tags } ) ]
    return found, modules


def _rel( path: Path ) -> str:
    try:
        return Path( os.path.relpath( path, _root() ) ).as_posix()
    except ValueError:
        return str( path )


def _list( argv ) -> int:
    tags, files = None, [ ]
    while argv:
        a = argv.pop( 0 )
        if a == "--tags":
            tags = argv.pop( 0 )
        else:
            files.append( a )
    found, _ = _read( files, tags )
    for e in found:
        params = "".join( f" --{n.replace( '_', '-' )}={p.default}" for n, p in e.params.items() )
        tagged = f" [{', '.join( e.tags )}]" if e.tags else ""
        print( f"{_rel( e.file )}::{e.name}" + ( f"\t{params.strip()}" if params else "" ) + ( f"\t#{tagged.strip()}" if tagged else "" ) )
    for path, short, _ in discovery.broken:
        print( f"{_rel( path )}: {short}", file = sys.stderr )
    return 1 if discovery.broken else 0


def _parameters( e, args ):
    """-> ( the values of the entry's declared parameters, what is left of the arguments ). Raises ValueError."""
    given, rest, i = { }, [ ], 0
    names = { "--" + n.replace( "_", "-" ): n for n in e.params }
    while i < len( args ):
        flag, eq, value = args[ i ].partition( "=" )
        if flag in names:
            if not eq:
                if i + 1 >= len( args ):
                    raise ValueError( f"{flag}: a value is missing" )
                i += 1
                value = args[ i ]
            given[ names[ flag ] ] = value
        else:
            rest.append( args[ i ] )
        i += 1
    return E.resolve_params( e.params, given ), rest


def _jsonable( v ):
    try:
        json.dumps( v )
        return v
    except ( TypeError, ValueError ):
        with contextlib.suppress( Exception ):
            return float( v )
        return str( v )


def _run( argv ) -> int:
    report, tags, wanted, args = None, None, [ ], [ ]
    while argv:
        a = argv.pop( 0 )
        if a == "--report":
            report = argv.pop( 0 )
        elif a == "--tags":
            tags = argv.pop( 0 )
        elif a == "--":
            args = argv
            break
        else:
            path, sep, name = a.partition( "::" )
            wanted.append( ( path, name if sep else None ) )
    files = list( dict.fromkeys( p for p, _ in wanted ) )
    found, modules = _read( files, tags )
    by_file = { }
    for e in found:
        by_file.setdefault( _rel( e.file ), [ ] ).append( e )
    chosen, missing = [ ], [ ]
    for path, name in wanted:
        here = by_file.get( path, [ ] )
        match = [ e for e in here if name is None or e.name == name ]
        chosen += [ e for e in match if e not in chosen ]
        if name is not None and not match:
            missing.append( f"{path}::{name}" )
    out = open( report, "a" ) if report and _rank() == 0 else None

    def said( name, status, seconds = 0.0, message = "", results = None, file = "" ):
        if out is not None:
            out.write( json.dumps( { "name": name, "status": status, "seconds": round( seconds, 6 ), "message": message,
                                     "results": results or { }, "file": file } ) + "\n" )
            out.flush()

    failed = skipped = 0
    for path, short, trace in discovery.broken:          # a file that will not import costs itself, never the others
        failed += 1
        print( f"{_rel( path )}: {short}\n{trace}", file = sys.stderr )
        said( "( read )", "FAIL", message = short, file = _rel( path ) )
    for name in missing:
        failed += 1
        there = [ e.name for e in by_file.get( name.partition( "::" )[ 0 ], [ ] ) ]
        print( f"{name}: no such entry" + ( f" -- there: {'  '.join( there[ : 8 ] )}" if there else "" ), file = sys.stderr )
        said( name.partition( "::" )[ 2 ], "FAIL", message = "no such entry" )
    if not chosen and not failed:
        print( "no entry was selected", file = sys.stderr )
        return 5
    base = Path( os.environ.get( "ERRAND_OUT_DIR" ) or ( Path( report ).parent if report else "out" ) ).resolve()
    root, ranks = _root(), int( os.environ.get( "ERRAND_RANKS" ) or 1 )
    asked = 0
    for e in chosen:
        leaf = base if len( chosen ) == 1 else base / _slug( e.name )
        leaf.mkdir( parents = True, exist_ok = True )
        try:
            resolved, rest = _parameters( e, args )
        except ValueError as err:
            failed += 1
            print( f"{e.name}: {err}", file = sys.stderr )
            said( e.name, "FAIL", message = str( err ), file = _rel( e.file ) )
            continue
        os.environ[ "ERRAND_ENTRY" ] = f"{_rel( e.file )}::{e.name}"
        status, message, again = "PASS", "", False
        kept_out, kept_argv = os.environ.get( "ERRAND_OUT_DIR" ), sys.argv
        started = time.perf_counter()
        try:
            need = int( e.resources.get( "ranks" ) or 1 )
            if need > ranks and os.environ.get( "ERRAND_ASKS" ):
                E.require( ranks = need )                # not given where the run is: it is run again with them
            E.begin_run( e, resolved, leaf )
            sys.argv = [ str( e.file ), *( f"--{n.replace( '_', '-' )}={v}" for n, v in resolved.items() ), *rest ]
            discovery.import_file( modules[ e.module ], root )
        except local.Skipped as s:
            status, message = "SKIP", s.reason
        except KeyboardInterrupt:
            raise
        except SystemExit as exc:
            if exc.code == 75:
                status, message, again = "SKIP", "asks for what this run does not have", True
            elif exc.code == 77:
                status, message = "SKIP", "exit 77"
            elif exc.code not in ( None, 0 ):
                status, message = "FAIL", f"exit {exc.code}"
        except BaseException as exc:
            status, message = "FAIL", f"{type( exc ).__name__}: {exc}"
            discovery.print_failure( exc )
        finally:
            sys.argv = kept_argv
            if kept_out is None:
                os.environ.pop( "ERRAND_OUT_DIR", None )
            else:
                os.environ[ "ERRAND_OUT_DIR" ] = kept_out
        seconds = time.perf_counter() - started
        results = { k: _jsonable( v ) for k, v in E.results.items() }
        E.results.clear()
        E.end_run()
        failed += status == "FAIL"
        skipped += status == "SKIP"
        asked += again
        if _rank() == 0:
            sys.stdout.flush()
            numbers = "".join( f" · {k} {v}" for k, v in results.items() )
            print( f"{status} {e.name}  {seconds * 1000:.0f} ms{numbers}" + ( f"  {message}" if message else "" ), flush = True )
        said( e.name, status, seconds, message, results, _rel( e.file ) )
    if out is not None:
        out.close()
    if failed:
        return 1
    if asked and len( chosen ) == asked:
        return 75
    return 77 if skipped and skipped == len( chosen ) else 0


def main( argv = None ) -> int:
    argv = list( sys.argv[ 1 : ] if argv is None else argv )
    if argv[ : 1 ] in ( [ "list" ], [ "run" ] ):
        try:
            return _list( argv[ 1 : ] ) if argv[ 0 ] == "list" else _run( argv[ 1 : ] )
        except Exception as exc:                             # a file that does not import
            discovery.print_failure( exc )
            return 1
    print( "python -m errand.held list FILE… | run --report R FILE[::NAME]… [-- ARGS…]", file = sys.stderr )
    return 2


if __name__ == "__main__":
    sys.exit( main() )
