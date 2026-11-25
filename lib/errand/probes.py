"""A service's probes that are functions of its declaration ( `errand.http_json( path, lambda r: … )` ), asked by the agent
that keeps the service ( ADR 0018 ):

    python -m errand.probes NAME [CWD]

A line in, JSON: `{"what": "busy" | "ready", "ports": {NAME: PORT}}`; a line out: `true`, `false`, or `null` when the probe
failed. One process for as long as the service runs.
"""
from __future__ import annotations

import json
import sys


def main( argv = None ) -> int:
    argv = list( sys.argv[ 1 : ] if argv is None else argv )
    from .serve import declared
    svc = declared( argv[ 1 ] if len( argv ) > 1 else None )[ 0 ].get( argv[ 0 ] )
    if svc is None:
        print( f"{argv[ 0 ]}: not declared here", file = sys.stderr )
        return 1
    for line in sys.stdin:
        try:
            asked = json.loads( line )
            got = bool( getattr( svc, asked[ "what" ] )( asked[ "ports" ] ) )
        except Exception:
            got = None
        print( json.dumps( got ), flush = True )
    return 0


if __name__ == "__main__":
    sys.exit( main() )
