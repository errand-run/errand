"""Rows: a line a run says once per step, whose pieces errand aligns with those of the lines before it.

    errand.row( "adjust", [ "ctr=1", "pts=1250", "r=0.005" ], [ "loss 35.2", "→ 0.0035" ], [ "3.4 s" ] )

The step ( the head ), then groups of pieces ( a string alone is a group of one ). A piece takes the column of the same piece
in the rows of the same head: same group, same name ( before `=` ), else same place in the group.

The line written carries both: an escape no terminal shows, `MARK { json } END`, then the line as it reads without errand.
Outside errand ( no `$ERRAND_STEPS` ), only the latter."""
from __future__ import annotations

import json
import os
import sys

MARK = "\x1b]errand-row;"           # an OSC: terminals read up to its end and show nothing
END  = "\x07"


def text( head: str, *groups ) -> str:
    """The line `row` writes, without its end: marked under errand."""
    groups = [ [ g ] if isinstance( g, str ) else [ str( c ) for c in g ] for g in groups ]
    shown = "  ".join( [ str( head ) ] + [ " ".join( g ) for g in groups if g ] )
    if not os.environ.get( "ERRAND_STEPS" ):
        return shown
    return MARK + json.dumps( [ str( head ), groups ] ) + END + shown


def row( head: str, *groups, file = None ):
    """A row ( see the module )."""
    print( text( head, *groups ), file = file or sys.stdout, flush = True )
