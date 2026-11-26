"""errand's Python with no errand: no program, no run -- what a library may call by hand, and what it gets."""
import os
import subprocess
import sys
import tempfile
from pathlib import Path

from errand import test

import errand

LIB = str( Path( errand.__file__ ).resolve().parents[ 1 ] )


def alone( code: str, cwd, **env ):
    """`code` in a Python that knows of no run and of no program"""
    e = { k: v for k, v in os.environ.items() if not k.startswith( "ERRAND" ) and k not in ( "PATH", "PYTHONPATH" ) }
    e.update( { "PATH": str( cwd ), "PYTHONPATH": LIB, "PYTHONIOENCODING": "utf-8", **env } )
    # ( the program of the environment the tests run in is beside its Python: hidden )
    head = "import sys, errand.launcher as L\nsys.executable = '/nowhere/python'\n"
    return subprocess.run( [ sys.executable, "-c", head + code ], cwd = cwd, env = e, stdout = subprocess.PIPE, stderr = subprocess.PIPE, text = True, encoding = "utf-8" )


if test( "with no errand: a step, a result are nothing; a view says where it is; a build directory is the project's" ):
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path( tmp ).resolve()
        r = alone( "import errand\nerrand.step( 'a', done = .5 ); errand.result( x = 1 )\nassert errand.require( gpus = 1 ) == [ ]\n"
                   "assert errand.view( 'v.html', title = 'V', content = '<p>' )\nassert errand.view( 'v.html' )\n"
                   "assert errand.view( 's.sviz', open = errand.Static() )\nprint( errand.out_dir(), errand.has_tag( 'cuda' ) )", tmp )
        assert r.returncode == 0 and r.stdout == "out True\n", r
        lines = r.stderr.splitlines()
        assert len( lines ) == 2 and "V  file:///" in lines[ 0 ] and "s.sviz  file:///" in lines[ 1 ] and "errand open" not in r.stderr, r.stderr
        assert ( tmp / "v.html" ).read_text() == "<p>"
        # a stream that has not the letter, or is closed: the program goes on
        r = alone( "import errand\nassert errand.view( 'v.html' )\nprint( 'on' )", tmp, PYTHONIOENCODING = "ascii:strict" )
        assert r.returncode == 0 and r.stdout == "on\n" and "v.html" in r.stderr, r
        r = alone( "import errand, sys\nsys.stderr = None\nassert errand.view( 'v.html' )\nprint( 'on' )", tmp )
        assert r.returncode == 0 and r.stdout == "on\n", r

        where = "import errand\nprint( errand.build_dir(), errand.build_dir( 'cmake' ), errand.build_dir( '../up' ), sep = '\\n' )"
        base = tmp / "errand-outputs" / "caches" / "build" / "local"
        ( tmp / "sub" ).mkdir()
        ( tmp / "errand-vars.py" ).write_text( "" )
        r = alone( where, tmp / "sub" )
        assert r.returncode == 0 and r.stdout.splitlines() == [ str( base / "main" ), str( base / "cmake" ), str( base / ".._up" ) ], r
        assert ( base / "cmake" ).is_dir()
        # another program of that name says nothing of use; a run in a place without errand knows its place
        fake = tmp / "sub" / "errand"
        fake.write_text( "#!/bin/sh\necho hello\n" )
        fake.chmod( 0o755 )
        if os.name != "nt":
            assert alone( where, tmp / "sub" ).stdout.splitlines()[ 0 ] == str( base / "main" )
        r = alone( where, tmp / "sub", ERRAND_BUILDS = str( tmp / "b" ), ERRAND_BUILD_DIR = str( tmp / "b" / "main" ) )
        assert r.stdout.splitlines()[ :2 ] == [ str( tmp / "b" / "main" ), str( tmp / "b" / "cmake" ) ], r


if test( "what declares environments may be named by the surroundings, and says what it read" ):
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path( tmp ).resolve()
        ( tmp / "mine.py" ).write_text( "import errand\ndef provide( root ):\n    said = root / 'places.txt'\n"
                                        "    errand.envs[ said.read_text().strip() ] = errand.Env( [ ] )\n    return [ said ]\n" )
        ( tmp / "places.txt" ).write_text( "there\n" )
        e = { **os.environ, "PYTHONPATH": os.pathsep.join( [ LIB, str( tmp ) ] ), "ERRAND_PROVIDERS": "mine:provide, nothing.there" }
        r = subprocess.run( [ sys.executable, "-m", "errand.declared", str( tmp ) ], env = e, stdout = subprocess.PIPE, stderr = subprocess.PIPE, text = True )
        import json
        doc = json.loads( r.stdout )
        assert doc[ "envs" ][ "there" ][ "provided" ] and [ f[ "path" ] for f in doc[ "files" ] ] == [ str( tmp / "places.txt" ) ], doc
        assert "nothing.there" in r.stderr, r.stderr


if test( "with no program: a file at a URL is fetched to the place the program has it, whole or not there" ):
    import hashlib
    import json
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path( tmp ).resolve()
        ( tmp / "here" ).mkdir()
        ( tmp / "a b.bin" ).write_bytes( b"data" * 1000 )
        url, sha = ( tmp / "a b.bin" ).as_uri(), hashlib.sha256( b"data" * 1000 ).hexdigest()
        get = lambda *a, **env: alone( f"import errand\nprint( errand.download( { ', '.join( map( repr, a ) ) } ) )", tmp / "here", **env )
        d = tmp / "here" / "errand-cache" / "downloads"
        want = d / sha / "a_b.bin"
        r = get( url )
        assert r.returncode == 0 and r.stdout.strip() == str( want ) and want.read_bytes() == b"data" * 1000, r
        index = json.loads( ( d / "by-url" / ( hashlib.sha256( url.encode() ).hexdigest()[ :32 ] + ".json" ) ).read_text() )
        assert index == { "url": url, "sha256": sha, "name": "a_b.bin" }, index
        ( tmp / "a b.bin" ).unlink()                                    # there already: not asked again, by its URL or by its hash
        assert get( url ).stdout.strip() == str( want ) and get( "file:///nowhere/a_b.bin", sha ).stdout.strip() == str( want )
        r = get( "file:///nowhere/other.bin" )
        assert r.returncode != 0 and "RuntimeError: file:///nowhere/other.bin" in r.stderr, r
        ( tmp / "c.bin" ).write_bytes( b"other" )
        r = get( ( tmp / "c.bin" ).as_uri(), sha.replace( sha[ 0 ], "0" ) )
        assert r.returncode != 0 and "as asked -- removed" in r.stderr, r
        assert not list( ( d / ".errand-work" / "parts" ).iterdir() ) and not list( ( d / ".errand-work" / "locks" ).iterdir() )
        r = get( "http://127.0.0.1:9/x" )                               # nobody answers there
        assert r.returncode != 0 and "RuntimeError: http://127.0.0.1:9/x" in r.stderr, r
        r = get( "http://127.0.0.1:9/x", ERRAND_NO_INTERNET = "1" )
        assert r.returncode != 0 and "ERRAND_NO_INTERNET" in r.stderr, r
        r = get( url, None, "../up" )
        assert r.returncode != 0 and "the name of a file" in r.stderr, r


if test( "a cache zone outside any project, a library's cache by hand, in a run, and with nowhere of one's own" ):
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path( tmp ).resolve()
        r = alone( "import errand\nprint( errand.cache_dir( '/data-cache' ) )", tmp )
        assert r.stdout.strip() == str( tmp / "data-cache" ) and ( tmp / "data-cache" ).is_dir(), r
        where = lambda **env: alone( "import errand\nprint( errand.library_cache( 'mylib' ) )", tmp, **{ "HOME": str( tmp / "home" ), "XDG_CACHE_HOME": "", **env } )
        if os.name != "nt":
            r = where()
            own = tmp / "home" / ( "Library/Caches/mylib" if sys.platform == "darwin" else ".cache/mylib" )
            assert r.returncode == 0 and r.stdout.strip() == str( own ) and not r.stderr, r
            assert ( own / "CACHEDIR.TAG" ).read_text().startswith( "Signature: 8a477f597d28d172789f06886806bc55" )
            r = where( ERRAND_CACHES = str( tmp / "caches" ) )
            assert r.stdout.strip() == str( tmp / "caches" / "mylib" ), r
            ( tmp / "file" ).write_text( "" )
            r = where( ERRAND_CACHES = str( tmp / "file" / "x" ) )
            assert r.stdout.strip() == str( own ) and "cannot be written" in r.stderr, r
            ( tmp / "t" ).mkdir()
            nowhere = { "HOME": str( tmp / "file" / "h" ), "XDG_CACHE_HOME": str( tmp / "file" / "c" ), "TMPDIR": str( tmp / "t" ) }
            last = tmp / "t" / f"mylib-cache-{os.getuid()}"
            r = where( **nowhere )
            assert r.stdout.strip() == str( last ) and last.stat().st_mode & 0o777 == 0o700, r
            last.chmod( 0o777 )                                          # others may write in it: what it holds is run
            r = where( **nowhere )
            assert r.returncode != 0 and "no cache directory for mylib" in r.stderr, r
        for bad in ( "", "..", "a/b" ):
            assert "the name of a library" in alone( f"import errand\nerrand.library_cache( {bad!r} )", tmp ).stderr


if test( "a step's line: the scales said, merged between two writes; what JSON cannot say left out" ):
    import json
    with tempfile.TemporaryDirectory() as tmp:
        journal = Path( tmp ) / "steps.jsonl"
        r = alone( "import errand\nerrand.step( 'a', residual = errand.Log( 1 ) )\nerrand.step( loss = float( 'nan' ), cfl = errand.Lin( 2 ), done = float( 'inf' ) )\n"
                   "errand.step( residual = errand.Log( .5 ) )", tmp, ERRAND_STEPS = str( journal ) )
        assert r.returncode == 0, r
        said = { }
        for line in journal.read_text().splitlines():
            one = json.loads( line, parse_constant = lambda word: 1 / 0 )         # NaN, Infinity: no JSON
            said.update( one, scales = { **said.get( "scales", { } ), **one.get( "scales", { } ) } )
        del said[ "t" ]
        assert said == { "text": "a", "residual": .5, "cfl": 2.0, "scales": { "residual": "log", "cfl": "lin" } }, said
