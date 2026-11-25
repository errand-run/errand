"""What errand's Python does where errand's program is not: the same places, the same files, so that a program finds the same
things whether errand runs it or not.

Only the standard library; nothing here is needed, nor read, where the program is."""
from __future__ import annotations

import hashlib
import json
import os
import stat
import sys
import tempfile
import time
from pathlib import Path

ZONE = "errand-cache/downloads"      # as the program has it ( ADR 0013 )
WORK = ".errand-work"
TAG  = "Signature: 8a477f597d28d172789f06886806bc55\n# made by errand: regenerated when missing\n"

_said: set = set()


def say( text: str ):
    """A word to whoever runs the program, once; never its failure."""
    if text in _said or sys.stderr is None:
        return
    _said.add( text )
    try:
        print( text, file = sys.stderr, flush = True )
    except ( OSError, ValueError, UnicodeError, AttributeError ):
        pass


def root_or_here() -> Path:
    """The project's root; here, in a directory nobody declared anything about: a project all the same."""
    from .local import project_root
    said = os.environ.get( "ERRAND_ROOT" )
    if said:
        return Path( said )
    try:
        return project_root()
    except FileNotFoundError:
        return Path.cwd()


# ── the cache of a library ───────────────────────────────────────────────────

def can_write( path: Path ) -> bool:
    """`os.access` lies ( Windows, read-only mounts ): a file is written"""
    try:
        path.mkdir( parents = True, exist_ok = True )
        probe = path / f".write_probe_{os.getpid()}"
        probe.write_text( "ok" )
        probe.unlink()
        return True
    except OSError:
        return False


def is_own( path: Path ) -> bool:
    """Whether `path`, in a directory everybody writes in, is a directory of this user's that nobody else writes in ( made so when
    it is not there ): what a cache holds is loaded, and run."""
    try:
        path.mkdir( mode = 0o700, exist_ok = True )
        st = path.lstat()
    except OSError:
        return False
    if not hasattr( os, "getuid" ):
        return True
    return stat.S_ISDIR( st.st_mode ) and st.st_uid == os.getuid() and not st.st_mode & 0o022


def tag( path: Path ) -> Path:
    """A cache says it is one ( https://bford.info/cachedir/ ): what copies or backs up a tree leaves it alone."""
    try:
        if not ( path / "CACHEDIR.TAG" ).exists():
            ( path / "CACHEDIR.TAG" ).write_text( TAG )
    except OSError:
        pass
    return path


def library_cache( name: str ) -> Path:
    if not name or name in ( ".", ".." ) or any( c in name for c in "/\\" ):
        raise ValueError( f"library_cache( {name!r} ): the name of a library" )
    given = os.environ.get( "ERRAND_CACHES" )
    if given:
        if can_write( Path( given ) / name ):
            return tag( Path( given ) / name )
        say( f"errand: {Path( given ) / name} ( ERRAND_CACHES ) cannot be written: another cache is used for {name}" )
    try:
        home = Path.home()
    except ( RuntimeError, KeyError ):           # an account with no home ( a container, a service )
        home = None
    if sys.platform == "darwin":
        own = home / "Library" / "Caches" / name if home else None
    elif os.name == "nt":
        base = os.environ.get( "LOCALAPPDATA" ) or ( str( home / "AppData" / "Local" ) if home else None )
        own = Path( base ) / name / "cache" if base else None
    else:
        base = os.environ.get( "XDG_CACHE_HOME" )
        own = Path( base ) / name if base else home / ".cache" / name if home else None
    if own and can_write( own ):
        return tag( own )
    last = Path( tempfile.gettempdir() ) / f"{name}-cache-{os.getuid() if hasattr( os, 'getuid' ) else 'shared'}"
    if is_own( last ) and can_write( last ):
        return tag( last )
    raise RuntimeError( f"errand: no cache directory for {name} can be written ( {own or 'no home directory'}, {last} )" )


# ── a file at a URL ──────────────────────────────────────────────────────────

def name_of( url: str ) -> str:
    from urllib.parse import unquote, urlparse
    name = unquote( urlparse( url ).path.rstrip( "/" ).rsplit( "/", 1 )[ -1 ] )
    name = "".join( c if c.isascii() and c.isalnum() or c in "._-+" or ord( c ) > 127 else "_" for c in name ).lstrip( "." )
    return name or "download"


def download( url: str, sha256: str | None, name: str | None, root ) -> Path:
    """As the program does: the file under its hash, one fetch of a URL at a time, whole or not there. With no way to the URL,
    nobody is asked: an error."""
    sha256 = sha256.lower() if sha256 else None
    d = Path( root ) / ZONE
    key = hashlib.sha256( url.encode() ).hexdigest()[ :32 ]
    name = name or name_of( url )
    if name in ( ".", ".." ) or any( c in name for c in "/\\" ):
        raise ValueError( f"download( {url!r}, name = {name!r} ): the name of a file" )
    index = d / "by-url" / f"{key}.json"

    def have():
        sha, called = sha256, name
        if not sha:
            try:
                got = json.loads( index.read_text() )
                sha, called = str( got[ "sha256" ] ), str( got.get( "name" ) or name )
            except ( OSError, ValueError, KeyError, TypeError ):
                return None
        p = d / sha / called
        return p if p.is_file() else None

    found = have()
    if found:
        return found
    if os.environ.get( "ERRAND_NO_INTERNET" ) and not url.startswith( "file:" ):
        raise RuntimeError( f"{url}: this place is said to have no way out ( ERRAND_NO_INTERNET ), and errand is not here to ask" )
    lock = d / WORK / "locks" / key
    lock.parent.mkdir( parents = True, exist_ok = True )
    while True:
        try:
            lock.mkdir()
            break
        except FileExistsError:
            try:
                if time.time() - lock.stat().st_mtime > 3600:      # of a run that died
                    lock.rmdir()
                    continue
            except OSError:
                continue
            time.sleep( 0.2 )
    part = d / WORK / "parts" / f"{name}.{os.getpid()}.{time.time_ns()}"
    try:
        found = have()                           # the one that was waited for
        if found:
            return found
        part.parent.mkdir( parents = True, exist_ok = True )
        h, size = hashlib.sha256(), 0
        from urllib.error import URLError
        from urllib.request import Request, urlopen
        try:
            with urlopen( Request( url, headers = { "User-Agent": "errand" } ), timeout = 60 ) as r, open( part, "wb" ) as f:
                want = r.headers.get( "Content-Length" )
                while True:
                    chunk = r.read( 1 << 20 )
                    if not chunk:
                        break
                    f.write( chunk )
                    h.update( chunk )
                    size += len( chunk )
        except URLError as err:                  # ( its refusals, `HTTPError`, among them )
            raise RuntimeError( f"{url}: {getattr( err, 'code', '' ) or ''} {err.reason}".replace( ":  ", ": " ) ) from None
        except ( OSError, ValueError ) as err:
            raise RuntimeError( f"{url}: {err}" ) from None
        if want and want.isdigit() and int( want ) != size:
            raise RuntimeError( f"{url}: cut after {size} bytes of {want}" )
        digest = h.hexdigest()
        if sha256 and digest != sha256:
            raise RuntimeError( f"{url}: its sha256 is {digest}, not {sha256} as asked -- removed" )
        ( d / digest ).mkdir( parents = True, exist_ok = True )
        os.replace( part, d / digest / name )
        index.parent.mkdir( parents = True, exist_ok = True )
        tmp = index.with_name( f".{index.name}.{os.getpid()}.part" )
        tmp.write_text( json.dumps( { "url": url, "sha256": digest, "name": name } ) )
        os.replace( tmp, index )
        return d / digest / name
    finally:
        try:
            part.unlink()
        except OSError:
            pass
        try:
            lock.rmdir()
        except OSError:
            pass
