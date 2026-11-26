"""errand's Python: what a project declares ( `errand-*.py` ), and what a run written in Python says ( ADR 0032, 0034 ).

What launches, waits, carries and records is a program ( `cmd/errand` ); this package is what it asks of Python. A file of
work imports from here and nothing else, and finding the entries of a tree imports every one of them: the names below are
standard library only, the declarations are reached lazily.
"""
from __future__ import annotations

from .entries import (          # noqa: F401  -- the public surface of a file of work
    Args,
    Param,
    bench,
    build_dir,
    cache_dir,
    download,
    entry,
    experiment,
    has_tag,
    library_cache,
    memo,
    out_dir,
    require,
    tag,
    test,
    track,
)
from .local import Skipped, have, need, skip, value   # noqa: F401
from .rows import row                                 # noqa: F401
from .steps import Lin, Log, Static, result, step, view  # noqa: F401

try:
    from ._version import __version__                 # written when it is built: the tag of a release, or what git says
except ImportError:
    __version__ = "0+source"

# Read when a default is needed: `errand.default_env = "gpu"` in an `errand-*.py`.
default_env: str | None = None
# What a bare `errand run` runs: `errand.default_run = [ "tests/**::*" ]`, targets from the project root.
default_run: list | None = None

_LAZY = {
    "main": "launcher",
    **dict.fromkeys( ( "configure", "Env", "envs", "providers" ), "config" ),
    **dict.fromkeys( ( "serve", "http", "Server", "ServeError" ), "servers" ),
    **dict.fromkeys( ( "runners", "Runner", "Errand", "Pytest", "Catch2", "Cargo", "Python", "Executable" ), "runner" ),
    **dict.fromkeys( ( "Vars", "Venv", "Micromamba", "Conda", "Uv", "Nix", "Guix", "Module", "Apptainer", "Docker", "Podman",
                       "Ssh", "Slurm", "Mpi", "Preload", "Missing", "Windows", "WindowsBox" ), "layers" ),
}

__all__ = [ "Args", "Lin", "Log", "Param", "Skipped", "Static", "bench", "build_dir", "cache_dir", "default_env", "default_run", "download", "entry",
            "experiment", "has_tag", "have", "library_cache", "memo", "need", "out_dir", "require", "result", "row", "skip", "step", "tag",
            "test", "track", "value", "view", *_LAZY ]

from typing import TYPE_CHECKING
if TYPE_CHECKING:                # the same names, for whoever cannot follow `__getattr__`
    from .config import Env, configure, envs, providers
    from .launcher import main
    from .layers import (
        Apptainer, Conda, Docker, Guix, Micromamba, Missing, Module, Mpi, Nix, Podman, Preload, Slurm, Ssh, Uv, Vars, Venv,
        Windows, WindowsBox,
    )
    from .runner import Cargo, Catch2, Errand, Executable, Pytest, Python, Runner, runners
    from .servers import ServeError, Server, http, serve


# What a server is comes with the request ( ADR 0040 )
_GONE = { **dict.fromkeys( ( "services", "Service", "http_json" ), "a server is asked for in full: errand.serve( … ), or errand serve [options] -- COMMAND" ),
          "sources": "errand serve source URL", **dict.fromkeys( ( "dev_sources", "Checkout" ), "errand serve dev REPOSITORY PATH" ) }


def __getattr__( name ):
    if name in _GONE:
        raise AttributeError( f"errand.{name} is not read any more: {_GONE[ name ]}" )
    if name in _LAZY:
        import importlib
        return getattr( importlib.import_module( f"errand.{_LAZY[ name ]}" ), name )
    raise AttributeError( f"module {__name__!r} has no attribute {name!r}" )


def __dir__( ):
    return sorted( { *__all__, *globals() } )
