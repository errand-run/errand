"""What a project declares is one document ( ADR 0031 ): written from what its files say, and refused where it is no data."""
import contextlib
import io
import json
import tempfile
from dataclasses import dataclass
from pathlib import Path

import errand
from errand import test
from errand import config as C, declared as D, layers as L, local, runner as R


@contextlib.contextmanager
def project( **files ):
    """a project's files, in a directory -> its root; what they declare, and what is read here of the suite's own, put back"""
    kept = dict( local._values ), list( local._files ), local._root
    with tempfile.TemporaryDirectory() as tmp:
        root = Path( tmp ).resolve()
        for name, text in files.items():
            ( root / f"errand-{name}.py" ).write_text( text )
        try:
            yield root
        finally:
            C.reset()
            local._values.clear()
            local._values.update( kept[ 0 ] )
            local._files, local._root = kept[ 1 : ]


def refused( fn, *said, kind = ValueError ):
    try:
        fn()
    except kind as err:
        for s in said:
            assert s in str( err ), err
    else:
        assert False, f"not refused: {said}"


ENVS = '''
import errand
lmo = errand.Ssh( host = "lmo", root = "/data/proj", options = [ "-o", "ConnectTimeout=5" ] )
errand.envs[ "plain" ] = errand.Env( [ errand.Vars( { "A": "1", "P": "{root}/src" } ) ], driver = "cpu", fp = "32|64" )
errand.envs[ "gpu" ] = errand.Env( [ lmo, errand.Micromamba( "torch", python = "3.12", pip = [ "torch", "-e", "." ] ),
                                     errand.Vars( lambda t: { "FTYPE": t.get( "fp", "64" ) } ), errand.Preload( "torch", learn = False ) ],
                                   tools = { "python": "python3.12" }, needs = { "gpus": 1, "gpu_ram": "75%" }, driver = "cuda", fp = "32|64" )
base = [ errand.Ssh( host = "login", user = "me", jump = [ "a", "b" ] ) ]
errand.envs[ "hpc" ] = errand.Env( base + [ errand.Module( "gcc", "openmpi" ), errand.Slurm( gpus = 1, qos = [ ( "2:00:00", "dev" ), ( None, "long" ) ] ),
                                            errand.Apptainer( image = "c/x.sif", build_on = errand.envs[ "gpu" ], flags = [ "--nv" ] ) ],
                                   prepare = base + [ errand.Slurm( cpus = 16 ) ] )
errand.default_run = [ "tests/**::*" ]
errand.configure( src = [ "lib" ], zones = { "/results": "back" } )
'''

if test( "a project's declarations are one document: every environment, its layers field by field, nothing but data" ):
    with project( envs = ENVS ) as root:
        doc = json.loads( json.dumps( D.document( root ) ) )
        assert doc[ "not_data" ] == [ ] and doc[ "v" ] == D.V and list( doc[ "envs" ] ) == [ "plain", "gpu", "hpc" ], doc[ "not_data" ]
        assert doc[ "first_env" ] == "plain" and doc[ "default_env" ] is None and doc[ "default_run" ] == [ "tests/**::*" ]
        assert doc[ "settings" ][ "src" ] == [ "lib" ] and doc[ "settings" ][ "zones" ] == { "/results": "back" }
        assert [ f[ "path" ] for f in doc[ "files" ] ] == [ "errand-envs.py" ]
        gpu = doc[ "envs" ][ "gpu" ]
        assert gpu[ "stack" ][ 0 ] == { "type": "Ssh", "host": "lmo", "root": "/data/proj", "options": [ "-o", "ConnectTimeout=5" ] }, \
            "a field left at its default is written, or one given is not"
        assert gpu[ "stack" ][ 1 ] == { "type": "Micromamba", "name": "torch", "python": "3.12", "pip": [ "torch", "-e", "." ] }
        assert gpu[ "stack" ][ 3 ] == { "type": "Preload", "modules": [ "torch" ], "learn": False }
        assert gpu[ "tools" ] == { "python": "python3.12" } and gpu[ "needs" ] == { "gpus": 1, "gpu_ram": "75%" }
        # a function of the tags: its answer for each value the tags declare, and for the declaration itself
        values = gpu[ "stack" ][ 2 ][ "values" ]
        assert values[ "by_tags" ] == [ "driver", "fp" ]
        assert { g[ "fp" ]: v[ "FTYPE" ] for g, v in values[ "of" ] } == { "32": "32", "64": "64", "32|64": "32|64" }, values
        hpc = doc[ "envs" ][ "hpc" ]
        assert hpc[ "prepare" ] == [ { "same": 0 }, { "type": "Slurm", "cpus": 16 } ], hpc[ "prepare" ]
        assert hpc[ "stack" ][ 0 ] == { "type": "Ssh", "host": "me@login", "options": [ "-J", "a,b" ] }
        assert hpc[ "stack" ][ 1 ][ "names" ] == [ "gcc", "openmpi" ] and hpc[ "stack" ][ 2 ][ "qos" ] == [ [ "2:00:00", "dev" ], [ None, "long" ] ]
        assert hpc[ "stack" ][ 3 ] == { "type": "Apptainer", "image": "c/x.sif", "build_on": "gpu", "flags": [ "--nv" ] }


if test( "what is not data is said, where and why; a class that only gives other defaults is data" ):
    with project( envs = '''
import errand
from dataclasses import dataclass
class Mine( errand.Venv ):
    def wrap( self, cmd, ctx ): return cmd
class Hop:
    def wrap( self, cmd, ctx ): return cmd
@dataclass
class Other( errand.Micromamba ):
    root_prefix: str = "/opt"
class Runs( errand.Runner ):
    def run( self, entry, ctx ): pass
class Said( errand.Runner ):
    def data( self ): return { **super().data(), "run": { "words": [ "x", "{file}" ] }, "report": { "form": "exit" } }
errand.envs[ "own" ] = errand.Env( [ Mine( python = "python3" ), Hop() ] )
errand.envs[ "other" ] = errand.Env( [ Other( "x" ) ] )
errand.envs[ "jz" ] = errand.Env( [ errand.Slurm( qos = lambda s: "dev" ), errand.Vars( { "A": object() } ) ] )
errand.envs[ "fails" ] = errand.Env( [ errand.Vars( lambda t: t[ "nope" ] ) ] )
errand.runners[ "runs" ] = Runs()
errand.runners[ "said" ] = Said()
''' ) as root:
        doc = D.document( root )
        where = { n[ "where" ]: n[ "why" ] for n in doc[ "not_data" ] }
        assert set( where ) == { "envs.own.stack[0]", "envs.own.stack[1]", "envs.jz.stack[0].qos", "envs.jz.stack[1].values.A", "envs.fails.stack[0].values" }, where
        assert "changes what Venv does ( wrap )" in where[ "envs.own.stack[0]" ] and "Hop" in where[ "envs.own.stack[1]" ]
        assert "a table says the same" in where[ "envs.jz.stack[0].qos" ] and "KeyError" in where[ "envs.fails.stack[0].values" ]
        assert doc[ "envs" ][ "other" ][ "stack" ] == [ { "type": "Micromamba", "name": "x", "root_prefix": "/opt" } ]
        assert "run" in doc[ "runners" ][ "runs" ][ "not_data" ] and "not_data" not in doc[ "runners" ][ "said" ], doc[ "runners" ][ "said" ]


if test( "a declaration that cannot be right is refused where it is written" ):
    env, ssh = C.Env, L.Ssh( host = "h" )
    refused( lambda: env( [ L.Apptainer( image = "x.sif" ), ssh ] ), "Ssh must be the first" )
    refused( lambda: env( [ L.WindowsBox() ] ), "must follow" )
    refused( lambda: env( [ L.Vars( { } ), L.Windows() ] ), "first layer" )
    refused( lambda: env( [ L.Slurm(), L.Slurm() ] ), "several schedulers" )
    refused( lambda: env( [ L.Docker() ] ), "name the image" )
    refused( lambda: env( [ L.Micromamba() ] ), "name the environment" )
    refused( lambda: env( [ ], fp = "32,64" ), "comma" )
    refused( lambda: env( [ ], needs = { "gpu": 1 } ), "one of cpus" )
    refused( lambda: env( [ ], needs = { "ram": "lots" } ), "16G" )
    refused( lambda: env( [ ], needs = { "ram": "75%" } ), "16G" )
    for fine in ( { "ram": "16G", "cpus": 4 }, { "gpus": 1, "gpu_ram": "75%" }, { "gpu_ram": "40GB" }, { "exclusive": True }, { "ram": 2048, "cpus": None } ):
        env( [ ], needs = fine )
    refused( lambda: L.Slurm( extra = [ "--ntasks=4" ] ), "--ranks" )
    refused( lambda: env( [ ], tools = { "cmake": "" } ), "a command" )
    refused( lambda: env( [ ssh, L.Windows() ], prepare = [ ssh, L.Windows( cpus = 2 ) ] ), "different machines" )
    refused( lambda: C.configure( outt = "runs" ), "outt", kind = TypeError )
    refused( lambda: C.configure( zones = { "../x": "back" } ), "not a path" )
    refused( lambda: errand.runners.__setitem__( "x", 3 ), "must be a runner", kind = TypeError )
    refused( lambda: C.envs.__setitem__( "y", "not an env" ), kind = TypeError )


if test( "this machine's values come first, then the providers, then the project's files, in their order" ):
    seen = { }

    def provider( root ):
        seen[ "root" ], seen[ "host" ] = root, errand.value( "far_host" )
        errand.envs[ "kit-a" ] = errand.Env( [ errand.Vars( { "FROM": "provider" } ) ] )
        errand.envs[ "kit-b" ] = errand.Env( [ errand.Vars( { "FROM": "provider" } ) ] )

    def broken( root ):
        raise RuntimeError( "no toolchain here" )

    said = io.StringIO()
    C.providers.extend( [ broken, provider ] )
    try:
        with project( vars = "far_host = 'over-there'\n", b = "import errand\nfrom .helpers import NAME\nerrand.envs[ NAME ] = errand.Env( [ ] )\n",
                      a = "import errand\nerrand.envs[ 'kit-a' ] = errand.Env( [ errand.Vars( { 'FROM': 'project' } ) ] )\n"
                          "errand.envs[ 'kit-b' ].stack.append( errand.Vars( { 'MORE': '1' } ) )\n" ) as root, contextlib.redirect_stderr( said ):
            ( root / "helpers.py" ).write_text( "NAME = 'one'\n" )
            doc = D.document( root )
            assert seen == { "root": root, "host": "over-there" } and list( doc[ "envs" ] ) == [ "kit-a", "kit-b", "one" ], doc[ "envs" ]
            assert doc[ "envs" ][ "kit-a" ][ "stack" ][ 0 ][ "values" ] == { "FROM": "project" } and len( doc[ "envs" ][ "kit-b" ][ "stack" ] ) == 2
            assert doc[ "first_env" ] == "kit-a" and doc[ "envs" ][ "kit-b" ][ "provided" ], "one a provider declared is never the default"
            ( root / "helpers.py" ).write_text( "NAME = 'another'\n" )          # ( of another size: Python tells a file that changed by it, and by the second )
            assert "another" in D.document( root )[ "envs" ], "a module beside is read again"
        assert "no toolchain here" in said.getvalue()
    finally:
        C.providers.clear()


if test( "the runners: added, replaced by kind, removed" ):
    with project( project = '''
import errand
errand.runners[ "bench" ] = errand.Pytest( dirs = [ "bench" ], zones = { "/htmlcov": "back" } )
errand.runners.replace( errand.Catch2( dir = "cpp", build = "make" ) )
del errand.runners[ "cargo" ]
''' ) as root:
        got = D.document( root )[ "runners" ]
        assert sorted( got ) == [ "bench", "catch2", "errand", "executable", "pytest", "python" ], got
        assert got[ "bench" ][ "takes" ][ "dirs" ] == [ "bench" ] and got[ "bench" ][ "zones" ] == { "/htmlcov": "back" }
        assert got[ "catch2" ][ "build" ] == [ "make" ] and got[ "bench" ][ "type" ] == "Pytest"


if test( "services are asked for in full: what declared them says so" ):
    refused( lambda: errand.services, "errand serve", kind = AttributeError )
    refused( lambda: errand.Checkout, "errand serve dev", kind = AttributeError )


if test( "tags, parameters, steps: what a run reads and says" ):
    import contextlib, io, os
    from errand import entries as E, launcher, rows, steps
    has = lambda expr, tags: E.matches( expr, tags )
    assert has( "a & !b | c=3", { "a": True } ) and not has( "a & !b", { "a": True, "b": True } ) and has( "c=3", { "c": 3 } ) and has( "", { } )
    p = { "n": E.Param( 3, choices = [ 3, 5 ] ), "on": E.Param( True ), "name": E.Param( None ) }
    assert E.resolve_params( p, { } ) == { "n": 3, "on": True, "name": None }
    assert E.resolve_params( p, { "n": "5", "on": "no", "name": "x" } ) == { "n": 5, "on": False, "name": "x" }
    refused( lambda: E.resolve_params( p, { "n": "4" } ), "not in" )
    refused( lambda: E.resolve_params( p, { "n": "x" } ), "cannot read" )
    with tempfile.TemporaryDirectory() as tmp:
        kept = { k: os.environ.get( k ) for k in ( steps.ENV, steps.RESULTS ) }
        program = launcher.program
        def no_program( ):
            raise FileNotFoundError( "no errand here" )
        try:
            for k in kept:
                os.environ.pop( k, None )
            said = io.StringIO()
            launcher.program = lambda: "errand"
            with contextlib.redirect_stderr( said ), contextlib.redirect_stdout( io.StringIO() ) as out:
                assert errand.view( "x.html", title = "X" ) and errand.view( "x.html" ) and errand.view( "a b.sviz", open = errand.Static() )
            lines = said.getvalue().splitlines()
            assert len( lines ) == 2 and lines[ 0 ].startswith( "  ↗ X  file:///" ) and "errand open '/" in lines[ 1 ] and out.getvalue() == "", lines
            launcher.program = no_program
            with contextlib.redirect_stderr( io.StringIO() ) as said:
                assert errand.view( "c.sviz", open = errand.Static() )
            assert said.getvalue().startswith( "  ↗ c.sviz  file:///" ), said.getvalue()
            assert rows.text( "h", [ "a", "b" ], "c" ) == "h  a b  c"
            os.environ.update( { steps.ENV: f"{tmp}/{steps.FILE}", steps.RESULTS: f"{tmp}/results.jsonl" } )
            for i in range( 50 ):
                errand.step( f"iteration {i}", done = i / 50, residual = 1 / ( i + 1 ) )
            with contextlib.redirect_stderr( io.StringIO() ) as said, contextlib.redirect_stdout( said ):
                assert errand.view( "a/v.html", content = "<p>", open = errand.Static(), last = "m.json" )
            assert said.getvalue() == "", "errand says it"
            errand.result( seconds = 1.5 )
            steps.flush()
            said = [ json.loads( l ) for l in open( f"{tmp}/{steps.FILE}" ) ]
            assert len( said ) < 10 and said[ -1 ][ "open" ] == [ { "static": "index.html" } ] and said[ -1 ][ "last" ] == [ "m.json" ], said
            assert said[ -1 ][ "text" ] == "iteration 49", "what was merged is written with what comes next"
            assert ( Path( tmp ) / "a/v.html" ).read_text() == "<p>" and json.loads( open( f"{tmp}/results.jsonl" ).read() ) == { "seconds": 1.5 }
            assert rows.text( "h", "c" ).startswith( rows.MARK )
        finally:
            launcher.program = program
            steps.flush()
            for k, v in kept.items():
                os.environ.pop( k, None ) if v is None else os.environ.__setitem__( k, v )
