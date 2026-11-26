"""errand-run: errand's program, as pip installs it. The standard library, and Go to compile.

    pip install .                           compiled, and put in place: what is typed is the program itself
    pip wheel --no-deps .                   the wheel for this system
    python3 packaging/backend.py wheels     the wheels for every system, in dist/
    python3 packaging/backend.py build      dist/errand, with the programs of the other systems beside, nothing installed

A wheel is for one system: its program is `bin/errand`. It also carries those of the other systems, in `share/errand`: a machine
reached by ssh is sent the one it runs, and nobody there needs a compiler.

Building is an act: a source that is modified changes nothing until one of these is run.
"""
import base64
import hashlib
import os
import shutil
import subprocess
import time
import sys
import tarfile
import tempfile
import zipfile
from pathlib import Path

ROOT    = Path( __file__ ).resolve().parents[ 1 ]
sys.path.insert( 0, str( ROOT / "lib" ) )
from version import version          # noqa: E402  -- one for the two distributions
sys.path.pop( 0 )
VERSION = version()
NAME    = f"errand_run-{VERSION}"
SYSTEMS = {
    "linux/amd64":   "manylinux_2_17_x86_64.manylinux2014_x86_64.musllinux_1_1_x86_64",
    "linux/arm64":   "manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64",
    "darwin/arm64":  "macosx_11_0_arm64",
    "darwin/amd64":  "macosx_11_0_x86_64",
    "windows/amd64": "win_amd64",
}


def program( system: str ) -> str:
    return "errand-" + system.replace( "/", "-" ) + ".exe" * system.startswith( "windows" )


BUILT = time.time_ns()     # ( when: of two builds of one version, the later one replaces the other's agent )


def compiled( into: Path ) -> dict:
    """The program of every system -> where each is"""
    if not shutil.which( "go" ):
        raise SystemExit( "errand is compiled by Go, which is not here ( https://go.dev ); a wheel of PyPI needs none" )
    out = { }
    for system in SYSTEMS:
        goos, arch = system.split( "/" )
        out[ system ] = into / program( system )
        subprocess.run( [ "go", "build", "-trimpath", "-ldflags",
                          f"-s -w -X github.com/errand-run/errand/internal/system.Version={VERSION} -X github.com/errand-run/errand/internal/system.Built={BUILT}",
                          "-o", str( out[ system ] ), "./cmd/errand" ], cwd = ROOT, check = True,
                        env = { **os.environ, "GOOS": goos, "GOARCH": arch, "CGO_ENABLED": "0" } )
    return out


def here( ) -> str:
    said = subprocess.run( [ "go", "env", "GOOS", "GOARCH" ], check = True, stdout = subprocess.PIPE, text = True ).stdout.split()
    system = "/".join( said )
    if system not in SYSTEMS:
        raise SystemExit( f"errand: no wheel for {system} ( {', '.join( SYSTEMS )} )" )
    return system


def metadata( ) -> bytes:
    readme = ( ROOT / "README.md" ).read_text()
    return ( f"Metadata-Version: 2.1\nName: errand-run\nVersion: {VERSION}\n"
             f"Summary: Run work here, in a container or on another machine, and bring back what it produced\n"
             f"Home-page: https://github.com/errand-run/errand\nAuthor: Hugo Leclerc\nLicense: MIT\nRequires-Python: >=3.8\n"
             f"Description-Content-Type: text/markdown\n\n{readme}" ).encode()


def wheel( where, system: str, programs: dict ) -> str:
    files = { f"{NAME}.data/scripts/errand" + ".exe" * system.startswith( "windows" ): programs[ system ].read_bytes() }
    for other, path in programs.items():
        if other != system:
            files[ f"{NAME}.data/data/share/errand/{path.name}" ] = path.read_bytes()
    info = f"{NAME}.dist-info"
    files[ f"{info}/METADATA" ] = metadata()
    files[ f"{info}/licenses/LICENSE" ] = ( ROOT / "LICENSE" ).read_bytes()
    files[ f"{info}/WHEEL" ] = ( "Wheel-Version: 1.0\nGenerator: errand\nRoot-Is-Purelib: false\n"
                                 + "".join( f"Tag: py3-none-{tag}\n" for tag in SYSTEMS[ system ].split( "." ) ) ).encode()
    record, name = "", f"{NAME}-py3-none-{SYSTEMS[ system ]}.whl"
    with zipfile.ZipFile( Path( where ) / name, "w" ) as z:
        for rel, data in files.items():
            one = zipfile.ZipInfo( rel )
            one.external_attr = ( 0o100755 if ".data/" in rel else 0o100644 ) << 16
            z.writestr( one, data, zipfile.ZIP_DEFLATED, 9 )
            record += f"{rel},sha256={base64.urlsafe_b64encode( hashlib.sha256( data ).digest() ).decode().rstrip( '=' )},{len( data )}\n"
        z.writestr( f"{info}/RECORD", record + f"{info}/RECORD,,\n" )
    return name


# ── what pip calls ( PEP 517, 660 ) ──────────────────────────────────────────

def get_requires_for_build_wheel( config_settings = None ):
    return [ ]


get_requires_for_build_editable = get_requires_for_build_sdist = get_requires_for_build_wheel


def build_wheel( wheel_directory, config_settings = None, metadata_directory = None ):
    with tempfile.TemporaryDirectory() as tmp:
        return wheel( wheel_directory, here(), compiled( Path( tmp ) ) )


build_editable = build_wheel          # ( a program is compiled: there is nothing of it to edit in place )


def build_sdist( sdist_directory, config_settings = None ):
    name = f"{NAME}.tar.gz"
    with tarfile.open( Path( sdist_directory ) / name, "w:gz" ) as t:
        for top in ( "cmd", "internal", "lib", "packaging", "go.mod", "go.sum", "pyproject.toml", "README.md", "LICENSE" ):
            t.add( ROOT / top, f"{NAME}/{top}", filter = lambda i: None if "__pycache__" in i.name else i )
        with tempfile.TemporaryDirectory() as tmp:
            ( Path( tmp ) / "PKG-INFO" ).write_bytes( metadata() )
            t.add( Path( tmp ) / "PKG-INFO", f"{NAME}/PKG-INFO" )
    return name


def main( argv ) -> int:
    dist = ROOT / "dist"
    dist.mkdir( exist_ok = True )
    if argv == [ "wheels" ]:
        with tempfile.TemporaryDirectory() as tmp:
            programs = compiled( Path( tmp ) )
            for system in SYSTEMS:
                print( wheel( dist, system, programs ) )
        return 0
    if argv == [ "build" ]:
        with tempfile.TemporaryDirectory( dir = dist ) as tmp:          # compiled, all of it, then put in place
            programs = compiled( Path( tmp ) )
            mine = Path( tmp ) / "errand"
            shutil.copy2( programs[ here() ], mine )
            for path in ( *programs.values(), mine ):
                os.replace( path, dist / path.name )
        print( dist / "errand" )
        return 0
    print( __doc__ )
    return 2


if __name__ == "__main__":
    sys.exit( main( sys.argv[ 1 : ] ) )
