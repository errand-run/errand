"""Layers: what leads from "run this program" to the actual process, as a project declares it. A stack reads outside-in --
`[ Ssh( … ), Apptainer( … ), Vars( … ) ]` is ssh around apptainer around the command. What each DOES is errand's program's
( `internal/layer` ): here is what can be said of one, and nothing else.

A class of the project's own can give a layer other defaults ( `class Mine( errand.Micromamba ): root_prefix: str = "/opt" `,
a dataclass ); one that defines a method is behaviour, and no declaration ( `declared.py` says so ).
"""
from __future__ import annotations

from dataclasses import dataclass, field


class Layer:
    """What every layer is."""


@dataclass
class Vars( Layer ):
    """Environment variables for the child.

    `values` may be a function of the selected tags, which is how one environment covers a whole dimension instead of being
    split into one per value: `Vars( lambda t: { "FTYPE": t.get( "fp", "64" ) } )`.

    `{root}` in a value is the project's root where the command runs: its copy on a machine reached by `Ssh`, itself here."""
    values: dict | object


@dataclass( init = False )
class Module( Layer ):
    """Lmod / environment modules: how a cluster picks a toolchain. `Module( "gcc/13", "openmpi" )`"""
    names: list

    def __init__( self, *names ):
        self.names = list( names )


@dataclass
class Nix( Layer ):
    """`nix develop <flake> -c …`."""
    flake: str = "."
    shell: str | None = None


@dataclass
class Guix( Layer ):
    """`guix shell -m <manifest> -- …`."""
    manifest: str | None = None
    packages: list = field( default_factory = list )


@dataclass
class Mpi( Layer ):
    """How the place starts several processes of one program: `mpiexec -n N`. Without this layer, the launcher is the
    `mpiexec` beside the interpreter of the place, else the one on its PATH. A cluster names its own:
    `Mpi( "srun", np = "--ntasks" )`."""
    launcher: str = "mpiexec"
    np      : str = "-n"                                 # the flag giving the number of processes
    flags   : list = field( default_factory = list )      # e.g. [ "--oversubscribe" ] for Open MPI


class Preload( Layer ):
    """Runs forked from a process that has already imported ( ADR 0005, step 7 ).

        errand.Preload( "torch", "jax" )      these, imported ahead, and what runs use ( learn )
        errand.Preload( learn = False )        nothing but what is named

    Each run is a fork of that process: a copy of its own, nothing left over from the one before; the project's own modules
    are always read again."""

    def __init__( self, *modules, learn = True ):
        self.modules, self.learn = list( modules ), bool( learn )

    def __repr__( self ):
        return f"Preload( {', '.join( map( repr, self.modules ) )}, learn = {self.learn} )"


# ── an interpreter ───────────────────────────────────────────────────────────

@dataclass
class Venv( Layer ):
    """A specific interpreter: a venv's python, or any installed one."""
    python      : str = "python"
    requirements: str | None = None
    pip         : list = field( default_factory = list )
    create      : bool = False          # make the venv if `python` is inside a missing one


@dataclass
class Micromamba( Layer ):
    """A micromamba environment, by name."""
    name        : str = ""          # empty only in a declaration something completes ( a package's own layers ): refused when used
    python      : str | None = None
    channels    : list = field( default_factory = lambda: [ "conda-forge" ] )
    packages    : list = field( default_factory = list )
    requirements: str | None = None
    pip         : list = field( default_factory = list )
    root_prefix : str | None = None     # where `-n <name>` resolves
    executable  : str | None = None     # the micromamba binary, when it is not on PATH -- e.g. on a compute node


@dataclass
class Conda( Micromamba ):
    """The same thing through `conda`."""


@dataclass
class Uv( Layer ):
    """A uv-managed venv in the project."""
    path        : str = ".venv"
    python      : str | None = None
    requirements: str | None = None
    pip         : list = field( default_factory = list )


# ── a container ──────────────────────────────────────────────────────────────

class Container( Layer ):
    """An image the command runs in, with its own interpreter."""


@dataclass
class Apptainer( Container ):
    image : str = ""            # empty only in a declaration something completes: refused when used
    recipe: str | None = None
    flags : list = field( default_factory = list )
    mounts: dict = field( default_factory = dict )
    pip   : list = field( default_factory = list )
    # How the IMAGE is built. `fakeroot` builds without root on a host that allows it; `scratch` is a directory with room:
    # apptainer unpacks whole layers, and the default /tmp is where a build dies halfway on a shared machine.
    fakeroot   : bool = False
    scratch    : str | None = None
    build_flags: list = field( default_factory = list )
    # A machine without internet cannot build an image whose recipe downloads anything: `build_on` is one that can ( an
    # `Ssh`, or an environment, of the same architecture ); the image is built there and carried to where it runs. `install`
    # is a shell line run where it runs once it is there ( `{image}`: its path in the project ), for a site that only
    # executes images from a place of its own -- and `run_from` is then the image as entered, expanded by the shell. `exe`:
    # `singularity` where that is what the site calls it.
    build_on   : object = None
    install    : str | None = None
    run_from   : str | None = None
    exe        : str = "apptainer"


@dataclass
class Docker( Container ):
    """A container, run through the docker CLI. `user` defaults to the caller's own: a run writes its results into the
    project, and a daemon-backed container writes them as root unless told otherwise."""
    image : str = ""
    recipe: str | None = None
    flags : list = field( default_factory = list )
    mounts: dict = field( default_factory = dict )
    pip   : list = field( default_factory = list )
    engine: str = "docker"
    user  : str | None = "caller"


@dataclass
class Podman( Docker ):
    """The same, rootless: it already runs as the caller."""
    engine: str = "podman"
    user  : str | None = None


# ── a batch system ───────────────────────────────────────────────────────────

@dataclass
class Slurm( Layer ):
    """A batch allocation, kept warm a moment and shared by the commands that fit in it; `sbatch` under `--batch`.

    EVERYTHING IS OPTIONAL, `partition` included: what is not stated is not passed, and Slurm applies its own default."""
    partition: str | None = None
    nodes    : int | None = None
    cpus     : int | None = None
    gpus     : int | None = None
    time     : str | None = None
    account  : str | None = None
    nodelist : str | list | None = None   # `node15`, `node[1-4]` or [ "node15", "node16" ]
    # A name; or a table, for the clusters whose qos is a matter of how long the job is -- up to which time, which name,
    # the last one for anything longer: [ ( "2:00:00", "dev" ), ( "20:00:00", "normal" ), ( None, "long" ) ]
    qos      : object = None
    # Waiting through `sbatch`, the job asked how it is going every `watch` seconds at most: for a cluster that does not
    # keep a connection open as long as the queue and the job last. Needs a time ( here, or `--time` ).
    watch    : float | None = None
    # How the ranks of a run that asks for several are spread over the nodes. Their number is the run's.
    ntasks_per_node: int | None = None
    # The plugin `srun` starts the ranks with ( `srun --mpi=` ). None: the one the MPI of the environment speaks.
    mpi      : str | None = None
    extra    : list = field( default_factory = list )
    # How long an allocation nothing uses is held ( "30m" ). None: a minute, for the next command; `0`: one job per command.
    keep_alive: str | float | None = None
    # What is asked of Slurm for the allocation, apart from the commands it serves ( ADR 0009 ): `{ "time": "2:00:00" }`.
    # Its keys are this layer's own.
    allocation: dict | None = None

    TASK_FLAGS = ( "--ntasks", "-n", "--ntasks-per-node" )

    def __post_init__( self ):
        for flag in self.extra:
            word = str( flag ).split( "=" )[ 0 ]
            if word in self.TASK_FLAGS or ( word.startswith( "-n" ) and word[ 2: ].isdigit() ):
                raise ValueError( f"Slurm( extra = [ {flag!r} ] ): how many processes is the run's to say ( `--ranks 8`, or "
                                  f"`ranks = 8` on the entry ), and how they spread over the nodes is "
                                  f"`Slurm( ntasks_per_node = … )`" )


# ── another machine ──────────────────────────────────────────────────────────

@dataclass
class Ssh( Layer ):
    """Must be the first layer. Everything after it runs on `host`.

    `options` are passed to ssh: a port, an identity file. `user` and `jump` say what a `~/.ssh/config` entry would ( `User`,
    `ProxyJump` ), so that an environment file works for whoever has none: `jump` is a host or a list of them, gone through
    in order, and ends up in `options` as `-J`."""
    host   : str
    root   : str | None = None          # where the project's copy is there; its own path, by default
    python : str = "python3"
    options: list = field( default_factory = list )
    also   : list = field( default_factory = list )   # sibling checkouts the work needs, pushed with the project
    # Where errand writes for the project over there ( runs, builds, state ): a scratch disk, say. The project's copy then
    # holds a link to it, by its usual name.
    errand_outputs_directory: str | None = None
    # Where errand keeps what is its own over there ( its program, its agent ), in place of `~/.cache/errand`: a disk of that
    # machine when its home is slow, or shared with machines that do not see it. `~` and `$NAME` are that machine's.
    cache  : str | None = None
    user   : str | None = None
    jump   : str | list | None = None
    # A machine shared with others ( ADR 0028 §7 ): what errand keeps there goes sooner, no port kept. None: as the machine
    # says ( Slurm there: shared ).
    shared : bool | None = None

    def __post_init__( self ):
        if self.user and "@" not in self.host:
            self.host = f"{self.user}@{self.host}"
        if self.jump:
            hops = [ self.jump ] if isinstance( self.jump, str ) else list( self.jump )
            self.options = [ "-J", ",".join( hops ), *self.options ]
        self.user = self.jump = None


@dataclass
class Windows( Layer ):
    """A Windows errand installs, boots under QEMU, and lets go of. First, or right after the `Ssh` that leads to the machine
    that boots it. Everything after it runs there. Most declarations name none of its fields."""
    iso      : str | None = None     # a path or a URL; None is the evaluation, which needs no licence for 180 days
    key      : str | None = None     # a product key; None is the evaluation, which needs none
    index    : int = 1               # which image of the ISO: 1 is Server Core on the evaluation
    cpus     : int = 4
    ram      : str = "8G"
    disk     : str = "64G"           # grows as it is written, up to this
    msvc     : bool = True           # the build tools, and vcvars64 before every command
    python   : str = "3.13.16"       # that python.org release, on its PATH
    provision: list = field( default_factory = list )   # PowerShell: a `.ps1` of the project, or a line
    fetch    : list = field( default_factory = list )   # what to bring back after each command
    also     : list = field( default_factory = list )   # sibling checkouts the work needs ( `../loom` )
    # how long it stays up once nothing uses it -- and only while nobody needs its cores
    keep     : str = "5m"
    data     : str | None = "32G"    # a disk kept from one boot to the next, as E:, where the project lands; None:
                                     # everything is C:, reset at each boot
    root     : str | None = None     # where the project goes


@dataclass
class WindowsBox( Layer ):
    """A Windows that is there, right after the `Ssh` that reaches it: everything after it runs there. What errand puts on it
    is all in `C:\\errand`. `python`: None runs the one the machine has; "3.13.1" has uv install that one. `msvc`: vcvars64
    before every command, the Build Tools being there."""
    python   : str | None = None
    msvc     : bool = False
    provision: list = field( default_factory = list )   # PowerShell, run by `errand setup`: a `.ps1` of the project, or a line
    fetch    : list = field( default_factory = list )
    also     : list = field( default_factory = list )
    root     : str | None = None                        # where the project goes; C:/errand/work/<project name>


@dataclass
class Missing( Layer ):
    """Stands for what this machine lacks to reach the environment: a package that declares environments for its users
    declares this one rather than nothing; selecting it stops with `what` is lacking and `hint`, what to write and where."""
    what: str
    hint: str | None = None


CLASSES = { c.__name__: c for c in list( globals().values() ) if isinstance( c, type ) and issubclass( c, Layer )
            and c not in ( Layer, Container ) }


def first( stack, kind ):
    """the first layer of a kind in a stack, or None"""
    return next( ( l for l in stack if isinstance( l, kind ) ), None )


def ssh_of( stack ):
    return first( stack, Ssh )


def vm_of( stack ):
    return first( stack, ( Windows, WindowsBox ) )


def container_of( stack ):
    return first( stack, Container )


def missing_of( stack ):
    return first( stack, Missing )
