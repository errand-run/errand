"""Services ( ADR 0018 ), as they are declared: long-lived processes errand starts on demand, never twice, and stops when
nobody uses them.

    errand.services[ "ollama" ] = errand.Service( [ "ollama", "serve" ], env = { "OLLAMA_HOST": "127.0.0.1:{port}" },
                                                  gpus = 1, gpu_ram = "10G", ports = 11435,
                                                  busy = errand.http_json( "/api/ps", lambda r: bool( r[ "models" ] ) ) )

Declared in `~/.config/errand/services.py` ( `$XDG_CONFIG_HOME` ), or in any project file errand reads.
"""
from __future__ import annotations

import json
import os
import re
import runpy
import shlex
from pathlib import Path

services: dict = { }

# Where `errand serve fetch` may take code from without a second word ( ADR 0027 §4 ): the user's to change in their file
sources: list = [ "https://pypi.org/simple", "conda-forge", "https://download.blender.org/" ]

# ADR 0028 §3: on this machine, a repository is represented by a checkout one works on; a page's services of that repository
# can run from it, as the checkout describes them: `errand.dev_sources[ "github.com/sdot-team/sdot" ] = errand.Checkout( … )`
dev_sources: dict = { }


class Checkout:
    """a checkout of a repository ( `dev_sources` ): its `path`, the env its Python services run in ( `env`: a conda or
    micromamba env's name, or a prefix ), and per machine another ( `envs`: { "lmo": "lmo-jax" } ), for its copies there"""

    def __init__( self, path, env: str | None = None, envs: dict | None = None ):
        self.path, self.env, self.envs = str( path ), env, dict( envs or { } )


def seconds( value, default: float ) -> float:
    """`"10m"`, `"30s"`, `"2h"`, `90` ( seconds ) -> seconds."""
    if value is None:
        return default
    m = re.fullmatch( r"\s*([0-9.]+)\s*(ms|s|m|min|h|d)?\s*", str( value ) )
    if not m:
        raise ValueError( f"a duration: 30s, 10m, 2h ( got {value!r} )" )
    return float( m.group( 1 ) ) * { None: 1, "ms": 1e-3, "s": 1, "m": 60, "min": 60, "h": 3600, "d": 86400 }[ m.group( 2 ) ]


NAME = r"[a-z][a-z0-9_]*"


def _port( name, spec ) -> tuple:
    """a port's spec -> ( announced, server ), None where errand chooses"""
    if isinstance( spec, ( tuple, list ) ) and len( spec ) == 2:
        sides = list( spec )
    else:
        m = re.fullmatch( r"\s*(\d*)\s*(?::\s*(\d*)\s*)?", "" if spec is None else str( spec ) )
        if not m:
            raise ValueError( f"port {name}: ANNOUNCED:SERVER, either side may be empty ( got {spec!r} )" )
        sides = list( m.groups() )
    sides = [ None if s in ( None, "" ) else int( s ) for s in sides ]
    if any( s is not None and not 0 < s < 65536 for s in sides ):
        raise ValueError( f"port {name}: {spec!r} is not a port" )
    return tuple( sides )


def parse_ports( ports ) -> dict:
    """`ports =` of a Service -> { name: ( announced, server ) }, in the order given"""
    got = { }
    for name, spec in ( ports if isinstance( ports, dict ) else { "port": ports } ).items():
        if not re.fullmatch( NAME, str( name ) ):
            raise ValueError( f"a port's name: lower case letters, digits, _ ( got {name!r} )" )
        got[ str( name ) ] = _port( name, spec )
    if not got:
        raise ValueError( "a service listens on one port at least" )
    fixed = [ p for sides in got.values() for p in sides if p is not None ]
    if len( fixed ) != len( set( fixed ) ):
        raise ValueError( f"ports {ports}: one number given twice ( errand listens on the announced side and relays to the server's )" )
    return got


class Service:
    """A server errand starts on demand and stops when nobody uses it.

    `command`: a list ( or a string, split as a shell would ). `ports`: { name: spec }, the ports it listens on; a spec is
    `"ANNOUNCED:SERVER"`, either side empty for errand to choose, or an int ( the announced side ), or None; a bare spec is
    one port named `port`. `{port:NAME}` in the command and in `env` is the server's side of port NAME, also in `$PORT_NAME`;
    `{port}` and `$PORT` when there is one port. `others`: let other accounts of the machine connect. `gpus`, `gpu_ram`,
    `ram`, `cpus`, `exclusive`: its claim, as for `errand run`. `busy`: a function of the server's ports -> whether the
    server is in the middle of something ( its claim is then firm ), or a shell command, exit 0 = busy; a probe that fails
    counts as busy. `ready`: a function of the server's ports -> whether it answers ( default: every port accepts ). `idle`:
    how long unused before it stops. `grace`: between asking it to stop and killing it. `cwd`: where it starts ( default:
    the home directory )."""

    def __init__( self, command, *, env = None, gpus = None, gpu_ram = None, ram = None, cpus = None, exclusive = False,
                  label = None, busy = None, ready = None, idle = "10m", ports = None, others = False, grace = "10s", cwd = None ):
        self.command = shlex.split( command ) if isinstance( command, str ) else [ str( c ) for c in command ]
        if not self.command:
            raise ValueError( "a service needs a command" )
        self.env   = { str( k ): str( v ) for k, v in ( env or { } ).items() }
        self.needs = { k: v for k, v in ( ( "gpus", gpus ), ( "gpu_ram", gpu_ram ), ( "ram", ram ), ( "cpus", cpus ) ) if v is not None }
        self.exclusive, self.others, self.label = bool( exclusive ), bool( others ), label
        self.busy, self.ready = busy, ready
        self.idle, self.grace = seconds( idle, 600.0 ), seconds( grace, 10.0 )
        self.ports = parse_ports( ports )
        self.cwd   = str( cwd ) if cwd else None
        for text in [ *self.command, *self.env.values(), *( [ busy ] if isinstance( busy, str ) else [ ] ) ]:
            for name in re.findall( r"\{port(?::(" + NAME + r"))?\}", text ):      # said now, not at start
                if ( name and name not in self.ports ) or ( not name and len( self.ports ) != 1 ):
                    raise ValueError( f"{{port{':' + name if name else ''}}}: no such port ( declared: {', '.join( self.ports )} )" )

    def data( self ) -> dict:
        probe = lambda f: f if f is None or isinstance( f, str ) else getattr( f, "data", None ) or { "function": True }
        return { "command": self.command, "env": self.env, "needs": self.needs, "exclusive": self.exclusive, "label": self.label,
                 "busy": probe( self.busy ), "ready": probe( self.ready ), "idle": self.idle, "grace": self.grace,
                 "ports": [ [ n, a, s ] for n, ( a, s ) in self.ports.items() ], "others": self.others, "cwd": self.cwd }


def http_json( path: str, predicate = bool, port: str | None = None ):
    """A probe: GET `http://127.0.0.1:<port><path>`, its JSON given to `predicate`. `port`: which port's server side
    ( default: the first ). It fails when the server does not answer, which counts as busy."""
    def probe( ports: dict ) -> bool:
        import urllib.request
        at = ports[ port ] if port else next( iter( ports.values() ) )
        with urllib.request.urlopen( f"http://127.0.0.1:{at}{path}", timeout = 5.0 ) as r:
            return bool( predicate( json.loads( r.read() or b"null" ) ) )
    if predicate is bool:                   # ( data: no Python is asked )
        probe.data = { "http_json": path, **( { "port": port } if port else { } ) }
    return probe


def declared( cwd = None ) -> tuple:
    """-> ( { name: ( Service, where ) } as services with a `source`, what was wrong )"""
    said: list = [ ]
    services.clear()
    dev_sources.clear()
    f = Path( os.environ.get( "XDG_CONFIG_HOME" ) or Path.home() / ".config" ) / "errand" / "services.py"
    if f.is_file():
        try:
            runpy.run_path( str( f ), run_name = "errand_services" )
        except Exception as err:            # one file that does not load hides nothing of the other
            said.append( f"{f}: {type( err ).__name__}: {err}" )
    machine = dict( services )
    dev = dict( dev_sources )
    from . import config, local
    try:
        config.load( local.project_root( cwd ) )
    except FileNotFoundError:
        pass
    except Exception as err:
        said.append( f"the project's files: {type( err ).__name__}: {err}" )
    dev_sources.clear()                     # ( a checkout is this machine's file's to declare, never a project's )
    dev_sources.update( dev )
    out = { }
    for name, svc in services.items():
        if not isinstance( svc, Service ):
            said.append( f"services[ {name!r} ]: not an errand.Service" )
            continue
        svc.source = "services.py" if machine.get( name ) is svc else "project"
        out[ name ] = svc
    return out, said


def document( cwd = None ) -> dict:
    got, said = declared( cwd )
    return { "v": 1, "services": { name: { **svc.data(), "source": svc.source } for name, svc in got.items() }, "sources": sources,
             "said": said, "dev": { k: vars( c ) for k, c in dev_sources.items() if isinstance( c, Checkout ) } }
