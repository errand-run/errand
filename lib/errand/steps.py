"""Steps ( ADR 0010 ): a run says where it is, and errand shows it live.

    errand.step( f"iteration {i}", done = i / n, residual = r )     # text, fraction, curves
    errand.view( "convergence.html", content = html )                # a file of the run that evolves
    errand.result( seconds = 1.52 )                                  # numbers kept in the run's record

Every call is one line of JSON in a file errand names in the run's surroundings ( `$ERRAND_STEPS`, `$ERRAND_RESULTS`; ADR
0032 ): at most one write per `RATE` seconds, the calls in between merged and written at the next one, or at exit. Outside
errand nothing is written and nothing raised; a view is said where it is, on the error output.
"""
from __future__ import annotations

import atexit
import json
import os
import shlex
import sys
import threading
import time
from pathlib import Path

ENV     = "ERRAND_STEPS"
RESULTS = "ERRAND_RESULTS"
FILE    = "steps.jsonl"
RATE    = 0.1                # seconds between two writes, at least


def _append( path, line: dict ):
    try:
        with open( path, "a" ) as f:          # one line, one write: several writers can share it
            f.write( json.dumps( line ) + "\n" )
    except OSError:
        pass                                  # a run must not fail over what it says


class _Journal:
    """The journal of the current run: calls merged between two writes."""

    def __init__( self, path: str ):
        self.path, self.pending, self.written, self.lock = path, { }, 0.0, threading.Lock()

    def add( self, event: dict, now = False ):
        with self.lock:
            self.pending.update( event )
            self.pending[ "t" ] = time.time()
            if now or self.pending[ "t" ] - self.written >= RATE:
                self.write()

    def write( self ):
        if self.pending:
            line, self.pending, self.written = self.pending, { }, time.time()
            _append( self.path, line )


_journal: _Journal | None = None


def _current( ) -> _Journal | None:
    """The journal `$ERRAND_STEPS` names now."""
    global _journal
    path = os.environ.get( ENV )
    if not path:
        return None
    if _journal is None or _journal.path != path:
        flush()
        _journal = _Journal( path )
    return _journal


@atexit.register
def flush( ):
    """Write what is merged and not written yet."""
    if _journal is not None:
        with _journal.lock:
            _journal.write()


def step( text: str | None = None, *, done: float | None = None, **values ):
    """Where the run is: `text` as a person reads it, `done` a fraction from 0 to 1, and named numbers, one curve each.
    Everything is optional; the text stays until another one is given."""
    event = { k: float( v ) for k, v in values.items() }     # a curve is made of numbers
    if text is not None:
        event[ "text" ] = str( text )
    if done is not None:
        event[ "done" ] = float( done )
    journal = _current()
    if journal is not None and event:
        journal.add( event )


def result( **values ):
    """Numbers, or words, kept in the run's record."""
    path = os.environ.get( RESULTS )
    if path and values:
        _append( path, values )


class Static:
    """What opens a view that is a directory carrying its own page ( ADR 0017 §5 ): `errand.view( "foam.sviz", open =
    errand.Static( "index.html" ) )`. Its files are served as they are, from the copy brought back here."""

    def __init__( self, index: str = "index.html" ):
        self.index = str( index )

    def wire( self ) -> dict:
        return { "static": self.index }

    def __repr__( self ):
        return f"errand.Static( {self.index!r} )"


_shown: set = set()              # outside errand: the views said, once each


def _list( v ) -> list:
    return list( v ) if isinstance( v, ( list, tuple ) ) else [ v ] if v else [ ]


def view( path, title: str | None = None, content: str | bytes | None = None, last = None, open = None ) -> bool:
    """`path` ( relative to the run's directory ) is a file of the run worth looking at as it evolves. With `content`,
    errand writes it, whole: a reader never sees half of it.

    `path` may be a DIRECTORY: named again, what changed in it comes back to whoever follows the run. `last`: a file of it
    ( or several ) that comes after the rest -- an index that names the other files never names one not there yet. `open`:
    what opens it ( `Static`, or a list, the first by default ).

    Where it opens is said, and not by the caller: under errand, by errand, on the machine one looks from; outside errand, a
    line on the error output, once per view. -> True."""
    journal = _current()
    target = Path( path )
    if not target.is_absolute() and journal is not None:
        target = Path( journal.path ).parent / target
    if content is not None:
        target.parent.mkdir( parents = True, exist_ok = True )
        tmp = target.with_name( f".{target.name}.tmp" )
        tmp.write_bytes( content if isinstance( content, bytes ) else content.encode() )
        tmp.replace( target )
    if journal is None:
        _show( target, title, open )
        return True
    event = { "view": str( path ), "title": title, "last": _list( last ),
              "open": [ o.wire() if hasattr( o, "wire" ) else o for o in _list( open ) ] }
    journal.add( { k: v for k, v in event.items() if v }, now = True )
    return True


def _show( target: Path, title, open ):
    """A view of a program run by hand: its name, and what opens it."""
    target = target.absolute()
    if str( target ) in _shown:
        return
    _shown.add( str( target ) )
    if sys.stderr is None:                    # ( a program with no console: `print` would take the output instead )
        return
    served = any( isinstance( o, Static ) for o in _list( open ) )      # its page reads what is beside it: a server opens it
    try:
        from .launcher import program
        if served:
            program()                         # ( when there is one to open it )
    except OSError:
        served = False
    line = f"{title or target.name}  {'errand open ' + shlex.quote( str( target ) ) if served else target.as_uri()}"
    for mark in ( "↗", "->" ):                # ( a stream that has not that letter )
        try:
            print( f"  {mark} {line}", file = sys.stderr, flush = True )
            return
        except UnicodeError:
            continue
        except ( OSError, ValueError, AttributeError ):
            return                            # a program must not fail over what it shows
