"""A version of errand, asked for from here, made and published by GitHub ( ADR 0037 ). `make release`, `make prerelease`, `make rehearse`.

    release       today's next version, as a tag on a commit of main that GitHub has: GitHub tests that commit on every system, makes
                  the packages once, installs and uses them, and publishes those, if all of it passes ( .github/workflows/release.yml )
    prerelease    the same, as a candidate: 2026.11.25.1rc1, which pip takes when it is told `--pre`
    rehearse      what can be done here of it: the tests, the packages, their installation. Nothing is tagged nor sent
    next          the version a release would be

A version that did not pass is never published, and its number is spent: the next one follows it.
"""
import datetime
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

ROOT  = Path( __file__ ).resolve().parents[ 1 ]
DIST  = ROOT / "dist"
NAMES = ( "errand-run", "errand-lib" )
TWINE = shlex.split( os.environ.get( "TWINE", "uvx twine" ) )
BUILD = shlex.split( os.environ.get( "PYBUILD", "uvx --from build pyproject-build" ) )
INDEX = "https://pypi.org"


def stop( why ):
    raise SystemExit( f"errand: {why}" )


def run( *words, **kw ):
    """a step, which has to work"""
    kw.setdefault( "stdin", subprocess.DEVNULL )
    print( "  " + " ".join( shlex.quote( str( w ) ) for w in words ), flush = True )
    if subprocess.run( [ str( w ) for w in words ], cwd = kw.pop( "cwd", ROOT ), **kw ).returncode:
        stop( f"{words[ 0 ]} failed: stopped there" )


def git( *words ) -> str:
    done = subprocess.run( [ "git", *words ], cwd = ROOT, stdout = subprocess.PIPE, stderr = subprocess.PIPE, text = True )
    if done.returncode:
        stop( f"git {' '.join( words )}: {done.stderr.strip()}" )
    return done.stdout.strip()


def title( text ):
    print( f"\n── {text} " + "─" * max( 2, 70 - len( text ) ), flush = True )


# ── the version ──────────────────────────────────────────────────────────────

def published( ) -> set:
    """every version of either package PyPI knows: a number is spent for ever, a file removed does not free it"""
    out = set()
    for name in NAMES:
        try:
            with urllib.request.urlopen( f"{INDEX}/pypi/{name}/json", timeout = 20 ) as r:
                out |= set( json.load( r ).get( "releases" ) or { } )
        except urllib.error.HTTPError as err:
            if err.code != 404:
                stop( f"{INDEX} does not say which versions are taken: {err}" )
        except OSError as err:
            stop( f"{INDEX} cannot be asked which versions are taken: {err}" )
    return out


def following( candidate = False ) -> str:
    """today's next version: after those that are tagged, and those that are published; a candidate, of the one to come"""
    d = datetime.date.today()
    day = re.escape( f"{d.year}.{d.month}.{d.day}" )
    taken = published() | { t[ 1 : ] for t in git( "tag", "--list", "v*" ).split() }
    count = 1 + max( [ int( m.group( 1 ) ) for v in taken for m in [ re.fullmatch( day + r"\.(\d+)", v ) ] if m ], default = 0 )
    v = f"{d.year}.{d.month}.{d.day}.{count}"
    if not candidate:
        return v
    return f"{v}rc{1 + max( [ int( m.group( 1 ) ) for t in taken for m in [ re.fullmatch( re.escape( v ) + r'rc(\d+)', t ) ] if m ], default = 0 )}"


# ── what is checked, and made ────────────────────────────────────────────────

def ready( ):
    """main, all of it committed, and GitHub's: what is tagged is what it tests"""
    branch = git( "symbolic-ref", "--short", "HEAD" )
    if branch != "main":
        stop( f"a release is of main, this is {branch}" )
    git( "rev-parse", "--verify", "HEAD" )
    if git( "status", "--porcelain" ):
        stop( "what is not committed would not be in the release ( git status )" )
    if "origin" not in git( "remote" ).split():
        stop( "there is no origin to push the tag to" )
    git( "fetch", "--quiet", "--tags", "origin" )
    if git( "ls-remote", "origin", "refs/heads/main" ).split( "\t" )[ 0 ] != git( "rev-parse", "HEAD" ):
        stop( "origin/main is not this commit: git push, or git pull" )


def tests( ):
    title( "the tests" )
    unformatted = subprocess.run( [ "gofmt", "-l", "cmd", "internal", "lib" ], cwd = ROOT, stdout = subprocess.PIPE, text = True ).stdout.split()
    if unformatted:
        stop( f"not formatted: {' '.join( unformatted )}" )
    run( "go", "vet", "./..." )
    run( "go", "vet", "./internal/...", "./lib", env = { **os.environ, "GOOS": "windows" } )
    run( "go", "test", "-count=1", "./..." )


def packages( v: str ) -> list:
    title( f"the packages of {v}" )
    shutil.rmtree( DIST, ignore_errors = True )
    env = { **os.environ, "ERRAND_VERSION": v }
    run( sys.executable, "packaging/backend.py", "wheels", env = env )
    run( *BUILD, "--outdir", DIST, "lib", env = env )
    out = sorted( p for p in DIST.iterdir() if p.suffix in ( ".whl", ".gz" ) )
    odd = [ p.name for p in out if f"-{v}-" not in p.name and not p.name.endswith( f"-{v}.tar.gz" ) ]
    if odd or len( out ) != 7:
        stop( f"five wheels of errand-run, a wheel and a source of errand-lib were expected, not {[ p.name for p in out ]}" )
    run( *TWINE, "check", "--strict", *out )
    return out


def installed( v: str ):
    """what was made, installed where nothing is, and used"""
    title( "installed, and used" )
    with tempfile.TemporaryDirectory( prefix = "er-", dir = "/tmp" if os.path.isdir( "/tmp" ) else None ) as tmp:      # ( short: a socket's path is )
        tmp = Path( tmp ).resolve()
        ( tmp / "q" ).mkdir( mode = 0o700 )
        ( tmp / "p" ).mkdir()
        env = { k: x for k, x in os.environ.items() if not k.startswith( "ERRAND" ) and k != "PYTHONPATH" }
        env.update( ERRAND_CACHE_DIR = str( tmp / "c" ), XDG_RUNTIME_DIR = str( tmp / "q" ), XDG_CONFIG_HOME = str( tmp / "config" ),
                    ERRAND_NO_ROUNDS = "1", ERRAND_KEEP_ALIVE = "1" )
        run( sys.executable, "-m", "venv", tmp / "v" )
        bin_ = tmp / "v" / ( "Scripts" if os.name == "nt" else "bin" )
        run( bin_ / "python", "-m", "pip", "install", "--quiet", "--disable-pip-version-check", "--no-index", "--find-links", DIST, *NAMES )
        ( tmp / "p" / "errand-envs.py" ).write_text( "import errand\nerrand.envs[ 'e' ] = errand.Env( [ errand.Vars( { 'E': 'set' } ) ] )\n" )
        ( tmp / "p" / "work.py" ).write_text( "import os, errand\nfrom errand import test\nif test( 'one' ):\n    print( 'said', errand.__version__, os.environ[ 'E' ] )\n" )
        for words, wanted in ( ( [ "--version" ], f"errand {v} " ), ( [ "run", "-q", "work.py::one" ], f"said {v} set" ) ):
            done = subprocess.run( [ str( bin_ / "errand" ), *words ], cwd = tmp / "p", env = env, stdin = subprocess.DEVNULL, stdout = subprocess.PIPE, stderr = subprocess.STDOUT, text = True )
            print( "  errand " + " ".join( words ) + ": " + done.stdout.strip() )
            if done.returncode or wanted not in done.stdout:
                stop( f"`errand {' '.join( words )}` ended with {done.returncode}, and {wanted!r} was expected" )
        run( bin_ / "python", "-c", f"import errand; assert errand.__version__ == {v!r}", cwd = tmp / "p", env = env )


def main( argv ) -> int:
    what = argv[ 0 ] if len( argv ) == 1 else ""
    if what == "next":
        print( following() )
        return 0
    if what == "rehearse":
        v = following()
        tests()
        packages( v )
        installed( v )
        print( f"\n{v} could be asked for; nothing was tagged nor sent" )
        return 0
    if what in ( "release", "prerelease" ):
        ready()
        v = following( candidate = what == "prerelease" )
        print( f"  {v}, of {git( 'log', '-1', '--format=%h %s' )}" )
        print( "  a tag is pushed; GitHub tests that commit and, if all of it passes, publishes it" )
        try:
            said = input( f"  {v}? type it to go on: " ).strip()
        except EOFError:
            said = ""
        if said != v:
            stop( "left there: nothing was tagged nor sent" )
        git( "tag", "-a", f"v{v}", "-m", f"errand {v}" )
        if subprocess.run( [ "git", "push", "origin", f"refs/tags/v{v}" ], cwd = ROOT ).returncode:
            git( "tag", "-d", f"v{v}" )
            stop( "the tag could not be pushed: it was taken off here as well" )
        at = re.sub( r"^git@([^:]+):", r"https://\1/", re.sub( r"\.git$", "", git( "remote", "get-url", "origin" ) ) )
        print( f"  asked for: {at}/actions" )
        return 0
    print( __doc__ )
    return 2


if __name__ == "__main__":
    sys.exit( main( sys.argv[ 1 : ] ) )
