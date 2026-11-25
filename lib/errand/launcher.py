"""errand's program, from Python: `errand.main( [ "run", "--env", "gpu", "job.py" ] )` -> its exit code. It is another
distribution ( `errand-run` ): the one of this environment, else the one on the path."""
from __future__ import annotations

import os
import shutil
import subprocess
import sys


def program( ) -> str:
    found = shutil.which( "errand", path = os.path.dirname( sys.executable ) ) or shutil.which( "errand" )
    if not found:
        raise FileNotFoundError( "errand's program is not here: pip install errand-run" )
    return found


def main( argv = None ) -> int:
    argv = list( sys.argv[ 1 : ] if argv is None else argv )
    # this Python reads the project's files: the packages that declare environments, or services, are its own
    return subprocess.run( [ program(), *argv ], env = { "ERRAND_DECLARE_PYTHON": sys.executable, **os.environ } ).returncode
