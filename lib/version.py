"""Which version this is: what is said ( `$ERRAND_VERSION`: a release, by its tag ), else what git says of the sources.

    2026.11.25.1                           the commit of a release, as it is
    2026.11.25.1+dev.3.g4f2a1b9.dirty      three commits after, and modified
    0+dev.12.g4f2a1b9                      no release yet
    0+source                               sources git says nothing of

PyPI takes no version with a `+`: what is not a release is published by nobody's mistake. A version is the day and a counter.
"""
import os
import re
import subprocess
from pathlib import Path

HERE = Path( __file__ ).resolve().parent
RELEASE = r"\d{4}\.\d{1,2}\.\d{1,2}\.\d+(rc\d+)?"


def _git( *words ) -> str:
    try:
        done = subprocess.run( [ "git", *words ], cwd = HERE, stdout = subprocess.PIPE, stderr = subprocess.DEVNULL, text = True )
    except OSError:
        return ""
    return done.stdout.strip() if done.returncode == 0 else ""


def version( ) -> str:
    said = os.environ.get( "ERRAND_VERSION", "" )
    if said:
        if not re.fullmatch( RELEASE, said ):
            raise SystemExit( f"ERRAND_VERSION = {said!r}: a version is a day and a counter, 2026.11.25.1 or 2026.11.25.1rc1" )
        return said
    for d in ( HERE, HERE.parent ):                      # ( a distribution of the sources )
        try:
            return re.search( r"^Version: (\S+)$", ( d / "PKG-INFO" ).read_text(), re.M ).group( 1 )
        except ( OSError, AttributeError ):
            pass
    got = re.fullmatch( rf"v({RELEASE})-(\d+)-(g[0-9a-f]+)(\.dirty)?",
                        _git( "describe", "--tags", "--long", "--match", "v[0-9]*", "--abbrev=7", "--dirty=.dirty" ) )
    if got:
        last, after, commit, dirty = got.group( 1 ), int( got.group( 3 ) ), got.group( 4 ), got.group( 5 ) or ""
        return last if not after and not dirty else f"{last}+dev.{after}.{commit}{dirty}"
    commit, after = _git( "rev-parse", "--short=7", "HEAD" ), _git( "rev-list", "--count", "HEAD" )
    if commit:
        return f"0+dev.{after}.g{commit}" + ".dirty" * bool( _git( "status", "--porcelain", "--untracked-files=no" ) )
    return "0+source"


if __name__ == "__main__":
    print( version() )
