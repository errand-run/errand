"""A server, asked for in full ( ADR 0040 ): errand gives it a port of this machine, starts it at the first connection, and stops it when
nobody uses it.

    ollama = errand.serve( [ "ollama", "serve" ], on = "lmo", vars = { "OLLAMA_HOST": "127.0.0.1:{port}" }, gpus = 1, gpu_ram = "10G",
                           busy = errand.http( "/api/ps", field = "models" ), idle = "2m", keep = "1d" )
    requests.post( ollama.url( "/api/chat" ), json = … )

Nothing is started by `serve`, nor asked before the first `url`. The url is asked of memory, and of errand again at half of the
lease: to be called before each request rather than kept. All of it goes through `errand serve --json`.
"""
from __future__ import annotations

import json
import os
import shlex
import subprocess
import threading
import time


class ServeError( RuntimeError ):
    """A server that errand refuses, or that does not start: why."""


def http( path: str, field: str | None = None, port: str | None = None ) -> dict:
    """A probe, for `busy` and `ready`: yes when `field` ( `a.b` goes inside ) of the JSON the server answers at `path` is there, and
    neither false, 0 nor empty. `port`: which of its ports ( default: the first )."""
    return { "http": path, **( { "field": field } if field else { } ), **( { "port": port } if port else { } ) }


def serve( command, *, on = None, vars = None, cwd = None, gpus = None, gpu_ram = None, ram = None, cpus = None, exclusive = False,
           ports = None, others = False, busy = None, ready = None, idle = None, grace = None, keep = None, label = None,
           version = None, version_of = ( ), listen = None, always = False, hold_end = None ) -> Server:
    """A server, by all that makes it. Two requests that say the same are one server.

    `command`: a list ( or a string, split as a shell would ). `{port}`, in it and in `vars`, is the server's side of its port
    ( `{port:NAME}` of several ). `on`: the machine it runs on, by its ssh name, or layers ( `[ errand.Ssh( "lmo" ), errand.Micromamba(
    "jax" ) ]` ); nothing: this one. `vars`: its variables; what surrounds them is a login shell's, never this program's. `cwd`:
    where it starts ( default: the home there ). `gpus`, `gpu_ram`, `ram`, `cpus`, `exclusive`: what it asks of the queue. `ports`:
    { name: spec }, a spec being `"ANNOUNCED:SERVER"`, either side empty, an int ( announced ) or None; a bare spec is one port.
    `others`: other accounts may connect. `busy`, `ready`: a line of shell, or `errand.http( … )`. `version_of`: files ( there )
    whose contents make another server; `version`: raised when something errand does not see has changed.

    Of its port, not of the server ( two requests that differ by these have two ports to one server ): `others`; `listen`, an address
    of this machine its port is listened on too ( `"0.0.0.0"`: other machines may connect; refused on a machine shared with others );
    `always`, the system listens on it in errand's stead, and starts errand at a connection, for clients that only have the number;
    `hold_end`, a client's end passed on once the server was silent that long after its answer.

    Not of what it is: `idle`, how long unused before it stops; `grace`, before it is killed; `keep`, how long its port stays
    ( a day ); `label`, what it is called."""
    if isinstance( command, str ):
        command = shlex.split( command )
    said = { "command": [ str( c ) for c in command ], "on": _on( on ), "vars": { str( k ): str( v ) for k, v in ( vars or { } ).items() },
             "cwd": None if cwd is None else str( cwd ), "gpus": gpus, "gpu_ram": gpu_ram, "ram": ram, "cpus": cpus, "exclusive": bool( exclusive ),
             "ports": _ports( ports ), "others": bool( others ), "busy": busy, "ready": ready, "idle": idle, "grace": grace, "keep": keep,
             "label": label, "version": version, "version_of": [ str( f ) for f in version_of ], "listen": listen, "always": bool( always ),
             "hold_end": hold_end }
    return Server( { k: v for k, v in said.items() if v not in ( None, False, [ ], { } ) or k == "command" } )


def _on( on ):
    if on is None or isinstance( on, str ):
        return on
    from . import config, declared, layers
    tags = None
    if isinstance( on, config.Env ):
        on, tags = on.stack, on.tags
    if isinstance( on, layers.Layer ):
        on = [ on ]
    w = declared.Writer()
    out = [ w.layer( layers.Ssh( l ) if isinstance( l, str ) else l, f"on[{i}]", tags ) for i, l in enumerate( on ) ]
    if w.not_data:
        raise TypeError( "; ".join( f"{n[ 'where' ]}: {n[ 'why' ]}" for n in w.not_data ) )
    return out


def _ports( ports ) -> list:
    out = [ ]
    for name, spec in ( ports if isinstance( ports, dict ) else { } if ports is None else { "port": ports } ).items():
        if isinstance( spec, ( tuple, list ) ):
            spec = ":".join( "" if s is None else str( s ) for s in spec )
        out.append( [ str( name ), None if spec is None else str( spec ) ] )
    return out


def _around( err, seen = None ):
    """an exception, and those it holds or comes from, as each library keeps them"""
    seen = set() if seen is None else seen
    if not isinstance( err, BaseException ) or id( err ) in seen:
        return
    seen.add( id( err ) )
    yield err
    for e in ( err.__cause__, err.__context__, getattr( err, "reason", None ), *err.args ):
        yield from _around( e, seen )


def _refused( err ) -> bool:
    return any( isinstance( e, ConnectionRefusedError ) for e in _around( err ) )


def _cut( err ) -> bool:
    """whether a connection was refused, or closed before its answer"""
    return any( isinstance( e, ConnectionError ) or type( e ).__name__ in ( "RemoteProtocolError", "ServerDisconnectedError", "IncompleteRead" )
                for e in _around( err ) )


class Server:
    """A server errand keeps: where it is, at each call ( `errand.serve` )."""

    def __init__( self, request: dict ):
        self.request = request
        self._lock, self._said, self._again = threading.Lock(), None, 0.0

    def __repr__( self ):
        return f"<errand server {self.request.get( 'label' ) or ' '.join( self.request[ 'command' ] )}>"

    def _run( self, *options ) -> dict:
        from .launcher import program
        done = subprocess.run( [ os.environ.get( "ERRAND" ) or program(), "serve", "--spec", "-", "--json", *options ],
                               input = json.dumps( self.request ), stdout = subprocess.PIPE, universal_newlines = True )
        try:
            said = json.loads( done.stdout )
        except ValueError:
            raise ServeError( f"errand did not answer ( exit {done.returncode} )" ) from None
        if "error" in said:
            raise ServeError( said[ "error" ] )
        return said

    def _keep( self, said: dict ) -> dict:
        self._said, self._again = said, time.time() + max( said[ "again_in" ], 1.0 )    # ( the clock: a laptop sleeps )
        return said

    def ask( self ) -> dict:
        """errand asked now, whatever is remembered: its answer ( `url`, `urls`, `ports`, `until`, `state`, `why`, … )"""
        with self._lock:
            return self._keep( self._run() )

    def _known( self ) -> dict:
        with self._lock:
            if self._said is None or time.time() >= self._again:
                self._keep( self._run() )
            return self._said

    def port( self, name: str | None = None ) -> int:
        """its port, on this machine ( the one of that name, when it has several )"""
        ports = self._known()[ "ports" ]
        if name is None:
            names = [ n for n, _ in self.request.get( "ports", [ ] ) ]
            name = names[ 0 ] if names else next( iter( ports ) )
        if name not in ports:
            raise KeyError( f"no port {name!r}: {', '.join( ports )}" )
        return int( ports[ name ] )

    def url( self, path: str = "", port: str | None = None ) -> str:
        """where it is reached, to be asked before each request"""
        return f"http://{self._known().get( 'address', '127.0.0.1' )}:{self.port( port )}{path}"

    def call( self, fn, path: str = "", port: str | None = None ):
        """`fn( url )`, once more when its connection is refused: errand is asked again meanwhile ( a machine that was restarted ). A
        server that does not start raises `ServeError`, which says why."""
        for again in ( True, False ):
            try:
                return fn( self.url( path, port ) )
            except Exception as err:
                if not _cut( err ):
                    raise
                said = self.ask()
                if said[ "state" ] in ( "failed", "away" ):
                    raise ServeError( f"{said[ 'label' ]}: {said.get( 'why' ) or said[ 'state' ]}" ) from err
                if not ( again and _refused( err ) ):
                    raise

    def state( self ) -> str:
        """how it is now: rests, waiting, starting, warm, in use, busy, failed, away"""
        return self.ask()[ "state" ]

    def wait( self ) -> Server:
        """started now, if it rests; returns once it answers"""
        with self._lock:
            self._keep( self._run( "--wait" ) )
        return self

    def _named( self, verb, *options ):
        from .launcher import program
        said = self._known()
        subprocess.run( [ os.environ.get( "ERRAND" ) or program(), "serve", verb, f"{said[ 'label' ]}~{said[ 'config' ]}", *options ],
                        stdout = subprocess.DEVNULL, check = True )

    def stop( self ):
        """it stops, for everybody; its port stays, and the next connection starts it again"""
        self._named( "stop" )

    def release( self, now: bool = False ):
        """its port is given back; it stops when nobody uses it ( `now`: at once, for everybody )"""
        self._named( "release", *( [ "--now" ] if now else [ ] ) )
        with self._lock:
            self._said = None
