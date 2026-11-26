"""errand's Python, a server asked for: what is said to errand, when it is asked again, what its failures are. The program is a fake one."""
import json
import os
import sys
import tempfile
import time
from pathlib import Path

from errand import test

import errand
from errand import servers


class fake:
    """a program in errand's place: what it is asked is kept, what it answers is said here"""
    def __init__( self, tmp, **answer ):
        self.log, self.out, self.was = Path( tmp ) / "asked", Path( tmp ) / "answer", os.environ.get( "ERRAND" )
        self.answer( **answer )
        self.log.write_text( "" )
        at = Path( tmp ) / "errand"
        at.write_text( f"#!{sys.executable}\nimport json, sys\nargs = sys.argv[ 1 : ]\n"
                       f"open( {str( self.log )!r}, 'a' ).write( json.dumps( [ args, json.loads( sys.stdin.read() ) if '--spec' in args else None ] ) + '\\n' )\n"
                       f"print( open( {str( self.out )!r} ).read() )\n" )
        at.chmod( 0o755 )
        os.environ[ "ERRAND" ] = str( at )

    def answer( self, **more ):
        self.out.write_text( json.dumps( { "url": "http://127.0.0.1:20001", "urls": { }, "ports": { "port": 20001 }, "again_in": 100, "until": 0, "state": "rests",
                                           "label": "x", "config": "abcd", **more } ) )

    def asked( self ):
        return [ json.loads( l ) for l in self.log.read_text().splitlines() ]

    def __enter__( self ):
        return self

    def __exit__( self, *_ ):
        os.environ.pop( "ERRAND" )
        if self.was is not None:
            os.environ[ "ERRAND" ] = self.was


if os.name != "nt" and test( "a server: what is asked of errand, and only when it has to be" ):
    with tempfile.TemporaryDirectory() as tmp, fake( tmp ) as f:
        s = errand.serve( "srv --port {port}", on = [ "lmo", errand.Micromamba( "jax" ) ], vars = { "N": 3 }, gpus = 1, busy = errand.http( "/api", field = "a.b" ), keep = "2h", label = "x" )
        assert f.asked() == [ ], "nothing is asked before its url is"
        assert s.url( "/a" ) == "http://127.0.0.1:20001/a" and s.url() == "http://127.0.0.1:20001" and s.port() == 20001
        [ [ args, said ] ] = f.asked()
        assert args == [ "serve", "--spec", "-", "--json" ]
        assert said == { "command": [ "srv", "--port", "{port}" ], "on": [ { "type": "Ssh", "host": "lmo" }, { "type": "Micromamba", "name": "jax" } ], "vars": { "N": "3" },
                         "gpus": 1, "busy": { "http": "/api", "field": "a.b" }, "keep": "2h", "label": "x" }, said
        s._again = time.time() - 1                       # its lease is half over
        s.url()
        assert len( f.asked() ) == 2
        s.stop()
        s.release( now = True )
        assert [ a for a, _ in f.asked() ][ 2 : ] == [ [ "serve", "stop", "x~abcd" ], [ "serve", "release", "x~abcd", "--now" ] ]

        several = errand.serve( [ "t" ], ports = { "http": 8000, "grpc": ( None, 18001 ), "m": None } )
        f.answer( ports = { "http": 8000, "grpc": 20002, "m": 20003 } )
        assert several.port() == 8000 and several.url( port = "grpc" ) == "http://127.0.0.1:20002"
        assert f.asked()[ -1 ][ 1 ][ "ports" ] == [ [ "http", "8000" ], [ "grpc", ":18001" ], [ "m", None ] ]
        try:
            several.port( "no" )
            assert False
        except KeyError:
            pass

        wide = errand.serve( [ "w" ], listen = "100.64.1.2", always = True, hold_end = "2s" )
        f.answer( address = "100.64.1.2" )
        assert wide.url( "/x" ) == "http://100.64.1.2:20001/x"
        assert { k: v for k, v in f.asked()[ -1 ][ 1 ].items() if k in ( "listen", "always", "hold_end" ) } == { "listen": "100.64.1.2", "always": True, "hold_end": "2s" }


if os.name != "nt" and test( "a server: a connection refused is asked for again, one that failed says why" ):
    with tempfile.TemporaryDirectory() as tmp, fake( tmp ) as f:
        s, seen = errand.serve( [ "x" ] ), [ ]

        def once( err ):
            def fn( url ):
                seen.append( url )
                if len( seen ) == 1:
                    f.answer( ports = { "port": 20009 } )
                    raise err
                return "done"
            return fn
        assert s.call( once( ConnectionRefusedError() ), "/a" ) == "done" and seen == [ "http://127.0.0.1:20001/a", "http://127.0.0.1:20009/a" ], seen

        class Wrapped( Exception ):                      # ( as the libraries keep it: `reason`, an argument, a cause )
            pass
        inner = Wrapped( "pool" )
        inner.reason = ConnectionRefusedError()
        seen.clear()
        assert s.call( once( Wrapped( inner ) ) ) == "done"

        for err in ( ConnectionResetError( "cut" ), ConnectionRefusedError() ):
            f.answer( state = "failed", why = "exit status 3" )
            try:
                s.call( lambda url: ( _ for _ in () ).throw( err ) )
                assert False
            except errand.ServeError as got:
                assert "exit status 3" in str( got ) and got.__cause__ is err
        f.answer()
        for err in ( ConnectionResetError( "cut" ), ValueError( "its own" ) ):      # one that answers otherwise: its caller's
            try:
                s.call( lambda url: ( _ for _ in () ).throw( err ) )
                assert False
            except ( ConnectionResetError, ValueError ) as got:
                assert got is err
        f.out.write_text( json.dumps( { "error": "no such program" } ) )
        try:
            errand.serve( [ "x" ] ).url()
            assert False
        except errand.ServeError as got:
            assert "no such program" in str( got )


if test( "a server: what cannot be said is refused" ):
    try:
        errand.serve( [ "x" ], on = [ object() ] )
        assert False
    except TypeError as err:
        assert "on[0]" in str( err )
    assert servers._ports( 8000 ) == [ [ "port", "8000" ] ] and servers._ports( None ) == [ ]
