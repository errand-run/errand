"""What only this machine can say: the plain values of the project's files ( conventionally `errand-vars.py`, not versioned,
read first ), and what an entry does without them -- it is SKIPPED, loudly, saying what to write and where.

    # errand-vars.py                         if test( "it runs over ssh" ):
    ssh_host = "gpu-box"                          host = need( "ssh_host", "a machine you can ssh to" )
"""
from __future__ import annotations

import inspect
from pathlib import Path

VARS_FILE = "errand-vars.py"

_values: dict = { }
_files: list = [ ]
_root: Path | None = None


class Skipped( Exception ):
    """Raised by `skip` and `need`: neither a failure nor a pass."""

    def __init__( self, reason, hint = None ):
        super().__init__( reason )
        self.reason = reason
        self.hint   = hint


def reset( files = ( ), root: Path | None = None ):
    global _files, _root
    _values.clear()
    _files, _root = list( files ), root


def offer( module ):
    """Keep what a project file defined that is plain data."""
    _values.update( { k: v for k, v in vars( module ).items() if not k.startswith( "_" )
                      and not inspect.ismodule( v ) and not inspect.isclass( v ) and not inspect.isroutine( v ) } )


def skip( reason, hint = None ):
    """Stop this entry, and say why."""
    raise Skipped( reason, hint )


def have( key: str ) -> bool:
    return key in _values


def value( key: str, default = None ):
    """The value of `key`, or `default`: what a project file asks, which has no option of not being read."""
    return _values.get( key, default )


def need( key: str, what: str = "", *, example = None ):
    """The value of `key`, or a skip that says what is missing."""
    if key in _values:
        return _values[ key ]
    shown = f"{key} = {example!r}" if example is not None else f"{key} = ..."
    where = ( _files[ 0 ].parent if _files else ( _root or Path( "." ) ) ) / VARS_FILE
    hint = f"add it to {where}" if where.is_file() else f"{where} does not exist yet. Create it (it is not tracked) with"
    raise Skipped( f"needs `{key}`" + ( f" -- {what}" if what else "" ), f"{hint}:\n    {shown}" )


def project_root( start = None ) -> Path:
    """The nearest directory above `start` ( the current one ) that holds a project file."""
    here = Path( start or Path.cwd() ).resolve()
    for d in ( here, *here.parents ):
        if any( p.is_file() for p in d.glob( "errand-*.py" ) ):
            return d
    raise FileNotFoundError( f"no project file ( errand-*.py ) in {here} or above: where does the project start?" )
