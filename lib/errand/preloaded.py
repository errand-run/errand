"""Warm starts for a run's Python ( the guide's "Preload" ), kept by the agent of the place ( ADR 0005, 0035 ):

    python -m errand.preloaded DIR KEEP LEARN [MODULE…]        ( on `$ERRAND_WARM_SOCKET` )

A process in the environment that never runs anybody's code: it imports, and it forks. A run started by `python …` there
hands it its standard streams, its directory and its surroundings; the fork becomes the run, with everything imported
already in memory -- torch, jax, a C++ extension -- and its own copy of all of it.

What to import is what is named, and, with LEARN, what runs turn out to use: one that ends says which modules it loaded, the
project's own aside ( read again by every run, as they may have changed ). Never a module whose import starts threads:
forking a process with threads leaves the fork with locks held by threads it does not have. A module whose file changes makes
this process go, once nothing it forked is still running; the next run starts another.

The memory is held in the machine's queue by the agent, which reads `rss`, and stops this process when somebody needs the
room. It goes with its agent, and after KEEP seconds unused.
"""
from __future__ import annotations

import array
import json
import os
import select
import signal
import socket
import sys
import time
from pathlib import Path


def _rss_mb( ) -> int:
    """What this process holds in memory, in MB."""
    try:
        return int( Path( "/proc/self/statm" ).read_text().split()[ 1 ] ) * os.sysconf( "SC_PAGE_SIZE" ) >> 20
    except ( OSError, ValueError, IndexError ):
        import resource
        peak = resource.getrusage( resource.RUSAGE_SELF ).ru_maxrss
        return peak >> ( 20 if sys.platform == "darwin" else 10 )


def _blas( ) -> bool:
    """Is an OpenBLAS mapped in this process? Its thread pool survives a fork: OpenBLAS stops it before one and starts it
    again after ( pthread_atfork )."""
    try:
        return "openblas" in Path( "/proc/self/maps" ).read_text().lower()
    except OSError:
        return False


def _threads( ) -> int:
    """Native threads of this process: what makes a fork unsafe."""
    task = Path( "/proc/self/task" )
    if task.is_dir():
        return len( list( task.iterdir() ) )
    import threading
    return threading.active_count()


def _alive( pid ) -> bool:
    try:
        os.kill( int( pid ), 0 )
    except PermissionError:
        return True
    except ( OSError, ValueError ):
        return False
    return True


def _kill( pid ):
    try:
        os.killpg( pid, signal.SIGKILL )
    except ( ProcessLookupError, PermissionError ):
        pass


class Preloaded:
    def __init__( self, where: Path, address: str, keep: float, learn: bool, modules: list ):
        self.where, self.address, self.keep, self.learn = where, address, keep, learn
        self.children: dict = { }         # pid -> the connection that asked for it
        self.loaded: dict = { }           # module -> ( file, mtime ) at import
        self.idle_since = time.monotonic()
        known = self._read( "modules.json", { } )
        self.never = list( known.get( "never" ) or [ ] )
        self.want = list( dict.fromkeys( [ *modules, *( known.get( "want" ) or [ ] if learn else [ ] ) ] ) )
        # what entering the environment added to the surroundings ( `micromamba run` ): a run's, which comes with those of
        # whoever typed it, has them too
        base = self._read( "base.json", { } )
        self.added = { k: v for k, v in os.environ.items() if base.get( k ) != v }

    # -- what is kept beside ------------------------------------------------

    def _read( self, name, otherwise ):
        try:
            return json.loads( ( self.where / name ).read_text() )
        except ( OSError, ValueError ):
            return otherwise

    def _write( self, name, text: str ):
        tmp = self.where / f".{name}.{os.getpid()}"
        tmp.write_text( text )
        os.replace( tmp, self.where / name )

    def _save( self ):
        self._write( "modules.json", json.dumps( { "want": self.want, "never": self.never } ) )

    # -- importing ----------------------------------------------------------

    def _learn( self ):
        for f in sorted( self.where.glob( "learned-*.json" ) ):
            names = self._read( f.name, [ ] )
            f.unlink()
            if self.learn:
                self.want += [ n for n in names if n not in self.want and n not in self.never ]
        self._save()

    def _wanted( self ):
        return next( ( n for n in self.want if n not in self.loaded and n not in self.never ), None )

    def _import( self, name ) -> bool:
        """-> False when this process may no longer fork: the import started threads -- other than an OpenBLAS's"""
        before, blas = _threads(), _blas()
        try:
            __import__( name )
        except BaseException:
            self.never.append( name )
            self._save()
            return True
        path = getattr( sys.modules.get( name ), "__file__", None )
        try:
            self.loaded[ name ] = ( path, os.stat( path ).st_mtime if path else None )
        except OSError:
            self.loaded[ name ] = ( None, None )
        if _threads() > before and not ( _blas() and not blas ):
            self.never.append( name )
            self._save()
            return False
        return True

    def _stale( self ) -> bool:
        for path, mtime in self.loaded.values():
            try:
                if path and os.stat( path ).st_mtime != mtime:
                    return True
            except OSError:
                return True
        return False

    # -- forking ------------------------------------------------------------

    def _fork( self, conn ):
        fds = array.array( "i" )
        data, anc, _, _ = conn.recvmsg( 1 << 20, socket.CMSG_SPACE( 3 * fds.itemsize ) )
        for level, kind, payload in anc:
            if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
                fds.frombytes( payload[ : len( payload ) - len( payload ) % fds.itemsize ] )
        while not data.endswith( b"\n" ):
            more = conn.recv( 1 << 20 )
            if not more:
                break
            data += more
        try:
            asked = json.loads( data.decode() )
            pid = os.fork()
            if pid == 0:
                self._child( asked, list( fds ) )
        except ValueError:
            conn.close()
            return
        finally:
            for fd in fds:
                os.close( fd )
        self.children[ pid ] = conn
        conn.sendall( json.dumps( { "pid": pid } ).encode() + b"\n" )

    def _child( self, asked, fds ):
        code = 1
        try:
            os.setsid()
            for fd, to in zip( fds, ( 0, 1, 2 ) ):
                os.dup2( fd, to )
            for fd in fds:
                os.close( fd )
            self.server.close()
            for c in self.children.values():
                c.close()
            signal.set_wakeup_fd( -1 )
            for sig in ( signal.SIGTERM, signal.SIGHUP, signal.SIGCHLD ):
                signal.signal( sig, signal.SIG_DFL )
            signal.signal( signal.SIGINT, signal.default_int_handler )
            os.chdir( asked[ "cwd" ] )
            os.environ.clear()
            os.environ.update( { **asked[ "env" ], **self.added } )
            for p in reversed( os.environ.get( "PYTHONPATH", "" ).split( os.pathsep ) ):
                if p and p not in sys.path:             # the run's own path: this process was started with another
                    sys.path.insert( 0, p )
            sys.stdout = os.fdopen( 1, "w", buffering = 1 )
            sys.stderr = os.fdopen( 2, "w", buffering = 1 )
            _reseed()
            code = _run( asked[ "request" ][ "argv" ] )
        except KeyboardInterrupt:
            code = 130
        except BaseException:
            import traceback
            traceback.print_exc()
        finally:
            try:
                if self.learn:
                    self._tell( asked[ "cwd" ] )
                import atexit
                atexit._run_exitfuncs()
                sys.stdout.flush()
                sys.stderr.flush()
            except BaseException:
                pass
            os._exit( code )

    def _tell( self, root: str ):
        """What this run imported that could be imported ahead: not the project's own, not errand's scan of it."""
        root, names = os.path.realpath( root ), [ ]
        for name, module in list( sys.modules.items() ):
            if module is None or name.startswith( ( "_errand_", "__main__" ) ):
                continue
            path = getattr( module, "__file__", None )
            if not ( path and os.path.realpath( path ).startswith( root + os.sep ) ):
                names.append( name )
        ( self.where / f"learned-{os.getpid()}.json" ).write_text( json.dumps( sorted( names ) ) )

    def _reap( self ):
        while self.children:
            try:
                pid, status = os.waitpid( -1, os.WNOHANG )
            except ChildProcessError:
                return
            if pid == 0:
                return
            conn = self.children.pop( pid, None )
            code = os.WEXITSTATUS( status ) if os.WIFEXITED( status ) else -os.WTERMSIG( status )
            if conn is not None:
                try:
                    conn.sendall( json.dumps( { "code": code } ).encode() + b"\n" )
                except OSError:
                    pass
                conn.close()

    # -- its life -----------------------------------------------------------

    def run( self ) -> int:
        import fcntl
        self.where.mkdir( parents = True, exist_ok = True )
        lock = open( self.where / "lock", "a" )
        try:
            fcntl.flock( lock, fcntl.LOCK_EX | fcntl.LOCK_NB )
        except OSError:
            return 0                                     # another is there
        if os.path.exists( self.address ):
            os.unlink( self.address )
        self.server = socket.socket( socket.AF_UNIX, socket.SOCK_STREAM )
        self.server.bind( self.address )
        os.chmod( self.address, 0o600 )
        self.server.listen( 16 )
        # a child that ends wakes the loop at once: its code is somebody's command waiting to end
        self.wake, w = os.pipe()
        os.set_blocking( w, False )
        signal.set_wakeup_fd( w )
        signal.signal( signal.SIGCHLD, lambda *_: None )
        signal.signal( signal.SIGTERM, lambda *_: sys.exit( 0 ) )
        self._learn()
        agent = os.environ.get( "ERRAND_AGENT_PID" )
        try:
            while self._turn( agent ):
                pass
        finally:
            self.server.close()
            if os.path.exists( self.address ):
                os.unlink( self.address )
        return 0

    def _turn( self, agent ) -> bool:
        """One turn of the loop. -> False when this process goes."""
        ready, _, _ = select.select( [ self.server, self.wake, *self.children.values() ], [ ], [ ], 1.0 )
        for r in ready:
            if r == self.wake:
                os.read( self.wake, 512 )
            elif r is self.server:
                conn, _ = self.server.accept()
                self.idle_since = time.monotonic()
                self._fork( conn )
            else:                                        # a client that went before its fork: SIGKILL
                try:
                    gone = not r.recv( 1, socket.MSG_PEEK )
                except OSError:
                    gone = True
                if gone:
                    for pid in [ pid for pid, c in self.children.items() if c is r ]:
                        _kill( pid )
        self._reap()
        if agent and not _alive( agent ):                # so is what it kept, and what it ran here
            for pid in list( self.children ):
                _kill( pid )
            return False
        if self.children:
            self.idle_since = time.monotonic()
            return True
        if any( r != self.wake for r in ready ):
            return True
        self._write( "rss", str( _rss_mb() ) )
        self._learn()                                    # idle: import what is wanted, until somebody asks for a fork
        if self._stale():
            return False
        while self._wanted() is not None and not select.select( [ self.server ], [ ], [ ], 0 )[ 0 ]:
            if not self._import( self._wanted() ):
                return False
        return time.monotonic() - self.idle_since <= self.keep


def _reseed( ):
    """A fork's random generators are its own: Python's `random` is reseeded by the fork itself."""
    for seed in ( lambda: sys.modules[ "numpy" ].random.seed(), lambda: sys.modules[ "torch" ].seed() ):
        try:
            seed()
        except Exception:
            pass


def _run( argv ) -> int:
    """A script, a module or a line, as `python` would run it -> its exit code"""
    import runpy
    try:
        if argv[ 0 ] == "-m":
            sys.argv = [ argv[ 1 ], *argv[ 2 : ] ]
            sys.path.insert( 0, os.getcwd() )
            runpy.run_module( argv[ 1 ], run_name = "__main__", alter_sys = True )
        elif argv[ 0 ] == "-c":
            sys.argv = [ "-c", *argv[ 2 : ] ]
            sys.path.insert( 0, "" )
            exec( compile( argv[ 1 ], "<string>", "exec" ), { "__name__": "__main__", "__builtins__": __builtins__ } )
        else:
            sys.argv = list( argv )
            sys.path.insert( 0, os.path.dirname( os.path.abspath( argv[ 0 ] ) ) )
            runpy.run_path( argv[ 0 ], run_name = "__main__" )
    except SystemExit as e:
        if isinstance( e.code, str ):
            print( e.code, file = sys.stderr )
        return e.code if isinstance( e.code, int ) else 0 if e.code is None else 1
    return 0


def main( argv = None ) -> int:
    argv = list( sys.argv[ 1 : ] if argv is None else argv )
    if len( argv ) < 3:
        print( "usage: python -m errand.preloaded DIR KEEP LEARN [MODULE…]", file = sys.stderr )
        return 2
    address = os.environ.pop( "ERRAND_WARM_SOCKET", "" ) or os.path.join( argv[ 0 ], "sock" )
    return Preloaded( Path( argv[ 0 ] ), address, float( argv[ 1 ] ), argv[ 2 ] == "1", argv[ 3 : ] ).run()


if __name__ == "__main__":
    sys.exit( main() )
