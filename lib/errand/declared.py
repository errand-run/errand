"""What a project declares, as one document ( ADR 0031 ): written in Python, read as data.

    python -m errand.declared [ROOT]              the document of the project at ROOT, as JSON

What a declaration holds that cannot be written down is listed ( `not_data`: where, and why ): what runs it refuses it,
saying which.
"""
from __future__ import annotations

import dataclasses
import hashlib
import itertools
import json
import sys
from pathlib import Path

from . import config, layers as L, local, runner

V = 1
# What writes the document: raised when it says more than it did, so that a document kept by an older writer is made again.
REV = 6


class Writer:
    def __init__( self ):
        self.not_data: list = [ ]

    def refuse( self, where: str, why: str ) -> dict:
        self.not_data.append( { "where": where, "why": why } )
        return { "not_data": f"kept:{len( self.not_data ) - 1}", "why": why }

    def value( self, v, where: str, tags = None ):
        if v is None or isinstance( v, ( str, int, float, bool ) ):
            return v
        if isinstance( v, L.Layer ):
            return { "layer": self.layer( v, where, tags ) }
        if isinstance( v, Path ):
            return str( v )
        if isinstance( v, config.Env ) and v.name:      # an environment where one is wanted ( `build_on` ): by its name
            return v.name
        if isinstance( v, dict ) and all( isinstance( k, str ) for k in v ):
            return { k: self.value( x, f"{where}.{k}", tags ) for k, x in v.items() }
        if isinstance( v, ( list, tuple ) ):
            return [ self.value( x, f"{where}[{i}]", tags ) for i, x in enumerate( v ) ]
        if isinstance( v, dict ):
            return self.refuse( where, "a dict whose keys are not strings" )
        if callable( v ):
            hint = " ( a table says the same: qos = [ ( \"2:00:00\", \"dev\" ), ( None, \"long\" ) ] )" if where.endswith( ".qos" ) else ""
            return self.refuse( where, f"a function{hint}" )
        return self.refuse( where, f"a {type( v ).__name__}: not a value that can be written down" )

    def layer( self, layer, where: str, tags = None ) -> dict:
        kind = type( layer )
        base = next( ( c for c in kind.__mro__ if L.CLASSES.get( c.__name__ ) is c ), None )
        if base is None:
            return self.refuse( where, f"a layer of a class of its own ( {kind.__name__} ), not one of errand's" )
        own = [ c for c in kind.__mro__ if c is not base and issubclass( c, base ) ]
        methods = sorted( { k for c in own for k, v in vars( c ).items() if callable( v ) and not k.startswith( "__" ) } )
        if methods:
            return self.refuse( where, f"{kind.__name__} changes what {base.__name__} does ( {', '.join( methods )} ): "
                                       f"behaviour is the orchestration's, not a declaration's" )
        out = { "type": base.__name__ }
        if not dataclasses.is_dataclass( base ):           # ( `Preload`: what it was given )
            return { **out, **{ k: self.value( v, f"{where}.{k}" ) for k, v in vars( layer ).items() if not k.startswith( "_" ) } }
        for f in dataclasses.fields( base ):
            v = getattr( layer, f.name )
            default = f.default_factory() if f.default_factory is not dataclasses.MISSING else f.default
            if v != default:
                out[ f.name ] = self.of_tags( v, f"{where}.{f.name}", tags ) if callable( v ) and base is L.Vars else \
                    self.value( v, f"{where}.{f.name}", tags )
        return out

    def of_tags( self, fn, where: str, tags ):
        """a function of the selected tags: its answer for every value the environment's tags declare -- those a tag that
        stands for several ( `"32|64"` ) stands for, and itself"""
        tags, table = tags or { }, [ ]
        values = [ [ *( p.strip() for p in v.split( "|" ) ), v ] if isinstance( v, str ) and "|" in v else [ v ]
                   for _, v in sorted( tags.items() ) ]
        for combo in itertools.product( *values ):
            given = dict( zip( sorted( tags ), combo ) )
            try:
                table.append( [ given, self.value( dict( fn( dict( given ) ) ), where ) ] )
            except Exception as err:
                return self.refuse( where, f"a function of the tags that fails for {given}: {type( err ).__name__}: {err}" )
        return { "by_tags": sorted( tags ), "of": table }

    def env( self, name: str, env ) -> dict:
        stack = [ self.layer( l, f"envs.{name}.stack[{i}]", env.tags ) for i, l in enumerate( env.stack ) ]
        prepare = None
        if env.prepare is not None:
            at = { id( l ): i for i, l in enumerate( env.stack ) }
            prepare = [ { "same": at[ id( l ) ] } if id( l ) in at else self.layer( l, f"envs.{name}.prepare[{i}]", env.tags )
                        for i, l in enumerate( env.prepare ) ]
        return { "stack": stack, "prepare": prepare, "provided": id( env ) in config.provided,
                 **{ k: self.value( getattr( env, k ), f"envs.{name}.{k}" ) for k in ( "tags", "tools", "needs" ) } }

    def runner( self, name: str, r ) -> dict:
        """A runner of a class of the project's own that does more than say what it is, is refused in its own record: a
        project is not refused for a runner no command uses."""
        kind = type( r )
        base = next( ( c for c in kind.__mro__ if c in runner.BUILTIN ), runner.Runner )
        out = { "type": base.__name__ }
        try:
            data = Writer()
            out.update( data.value( r.data(), f"runners.{name}" ) )
            if data.not_data:
                out[ "not_data" ] = "; ".join( f"{n[ 'where' ]}: {n[ 'why' ]}" for n in data.not_data )
        except Exception as err:
            out[ "not_data" ] = f"what it is cannot be written down: {type( err ).__name__}: {err}"
        own = [ c for c in kind.__mro__ if c is not base and issubclass( c, base ) ]
        methods = sorted( { k for c in own for k, v in vars( c ).items() if callable( v ) and k != "data" and not k.startswith( "__" ) } )
        if methods or base is runner.Runner and "data" not in { k for c in own for k in vars( c ) }:
            out[ "not_data" ] = ( f"{kind.__name__} is a runner of a class of its own"
                                  + ( f", which changes {', '.join( methods )}" if methods else ", which says no `data()`" )
                                  + ": what a runner does is the orchestration's; as data, it is what it takes, its commands "
                                    "and its report ( ADR 0036 )" )
        return out


def document( root: Path ) -> dict:
    import errand
    files = [ ]
    for p in config.config_files( root ):
        st = p.stat()
        files.append( { "path": p.name, "size": st.st_size, "mtime": st.st_mtime, "sha256": hashlib.sha256( p.read_bytes() ).hexdigest() } )
    config.load( root )
    for p in dict.fromkeys( config.read_too ):          # what the providers read: said by its whole path
        p = p if p.is_absolute() else Path( root ) / p
        if p.is_file():
            st = p.stat()
            files.append( { "path": str( p ), "size": st.st_size, "mtime": st.st_mtime, "sha256": hashlib.sha256( p.read_bytes() ).hexdigest() } )
    w = Writer()
    envs = { name: w.env( name, env ) for name, env in config.envs.items() }
    return { "v": V, "rev": REV, "errand": errand.__version__, "root": str( root ),
             "files": files, "settings": w.value( dataclasses.asdict( config.settings ), "settings" ),
             "default_env": errand.default_env, "default_run": w.value( errand.default_run, "default_run" ),
             "first_env": next( ( n for n, e in envs.items() if not e[ "provided" ] ), None ),      # the default, when none is named
             "envs": envs, "runners": { name: w.runner( name, r ) for name, r in runner.runners.items() }, "not_data": w.not_data }


def main( argv = None ) -> int:
    argv = list( sys.argv[ 1 : ] if argv is None else argv )
    out, sys.stdout = sys.stdout, sys.stderr                # ( what a project's file prints is not the document )
    doc = document( Path( argv[ 0 ] ).resolve() if argv else local.project_root() )
    print( json.dumps( doc, indent = 1 ), file = out )
    return 0


if __name__ == "__main__":
    sys.exit( main() )
