# Containers: Apptainer, Docker, Podman

A container layer runs the command inside an image, with the image's own interpreter. The image is
[built from its recipe](../upkeep) when it is missing or the recipe changed.

```python
errand.envs[ "gpu" ] = errand.Env( [ errand.Apptainer( image = "containers/cuda.sif", recipe = "containers/cuda.def",
                                                       flags = [ "--nv" ], pip = [ "jax[cuda13]" ] ) ] )
```

```bash
errand run --env gpu python train.py     # apptainer exec --nv containers/cuda.sif python train.py
```

## Apptainer

```python
Apptainer( image, recipe = None, flags = [ ], mounts = { }, pip = [ ],
           fakeroot = False, scratch = None, build_flags = [ ],
           build_on = None, install = None, run_from = None, exe = "apptainer" )
```

Runs the command with `apptainer exec`.

| | |
|---|---|
| `image` | the `.sif`, relative to the project root. With a `recipe`, optional: `errand-outputs/images/<recipe>.sif` |
| `recipe` | the `.def` it is built from. A change to it, or to a file its `%files` copies, is a reason to rebuild |
| `flags` | passed to `apptainer exec`: `--nv`, `--nvccli` |
| `mounts` | `{ path in the project: path in the container }` |
| `pip` | installed **into** the image |
| `exe` | the command: `singularity` where that is what the site calls it |

### Building the image

- `fakeroot`: build without root, on a host that allows it.
- `scratch`: a directory with room. Apptainer unpacks whole layers, and the default `/tmp` is where a
  build dies halfway, out of space, on a shared machine.
- `build_flags`: passed to `apptainer build`.

An image is built **where it is used**: with an [`Ssh`](./ssh) in front, the recipe is copied over
and the build happens there. See [Where an image gets built](../upkeep#where-an-image-gets-built).

### A cluster without internet

A recipe that downloads anything cannot be built on a machine without network access, which is the
usual national cluster.

- `build_on`: the name of an environment of the project -- `errand.envs[ "builder" ]` as well --, or
  the layers of one written in place ( `build_on = [ errand.Ssh( host = "lab-server" ) ]` ), on a
  machine that can build it, of the same architecture. It is a step of preparing, shown when a stack
  holds such an image: the image is built there by a **run in that environment** ( its layers, its
  variables, its queue ), then streamed through this machine to where it runs: the two need not
  reach each other. Where it is built does not make it stale: an image carried is kept current like
  one built in place, rebuilt in the builder when its recipe changes, and **not built again** when the
  builder already has it as it should be. An image that was changed outside errand while its recipe
  did not is kept as it is; when its recipe changed too, errand says so and replaces nothing until
  `errand setup --force` ( the one replaced is kept as `.prev` ).
- `install`: a shell line run where it runs once the image is there ( `{image}` is its path in the
  project ), for a site that only executes images from a place of its own
  ( `idrcontmgr cp {image}` at IDRIS ).
- `run_from`: the image as entered, when it is not `image`, expanded by the shell where it runs
  ( `$SINGULARITY_ALLOWED_DIR/main.sif` ).

```python
errand.envs[ "builder" ] = errand.Env( [ errand.Ssh( host = "lab-server", root = "/home/me/proj" ) ] )

errand.Apptainer( image = "containers/main.sif", recipe = "containers/main.def", build_on = "builder",
                  install = "idrcontmgr cp {image}", run_from = "$SINGULARITY_ALLOWED_DIR/main.sif",
                  exe = "singularity" )
```

### Several ranks

Under Slurm, the ranks of an MPI run start outside the image, with `srun`, and each enters it on its
node: `srun … apptainer exec image.sif python …`. The MPI of the image must speak the PMI that `srun`
offers ( PMIx for Open MPI, PMI-2 for MPICH ). `--cleanenv` or `--containall` in `flags` hides from
errand the agent that starts them, and the run is then skipped.
→ [Several processes, in a container](../mpi#in-a-container)

## Docker, Podman

```python
Docker( image, recipe = None, flags = [ ], mounts = { }, pip = [ ], engine = "docker", user = "caller" )
Podman( … )     # the same, through podman, rootless
```

Runs the command with `docker run --rm`. `image` is a tag ( `myproj:1` ), `recipe` the
`Dockerfile` it is built from; a change to it, or to what its `COPY` and `ADD` take from the project, rebuilds it.

An image errand builds from a recipe is errand's: one it has no record of having built is built again, the one there
kept ( `.prev` for an Apptainer image ), never adopted ( [ADR 0044](../../adr/0044-what-a-layer-builds-is-named-by-what-defines-it) ).

`user` defaults to your own uid and gid. A run writes its results into the project, and a
daemon-backed container writes as root unless told otherwise: the next run outside the container
could not clear them. `user = None` leaves the image's user; a string is passed to `--user`. Podman,
rootless, already runs as you, and passes no `--user`.

## What the place must have

Before anything is built, errand checks the place: `Apptainer` needs its `exe`; `Docker` and `Podman`
their engine, and that `docker info` ( or `podman info` ) answers. A failure stops the command and
says what is missing, and on which machine:

```text
  box: on build-box: apptainer, not found ( apptainer:mylib.sif )
```

A Docker or Podman container does not see the home directory where errand puts its own sources on
a machine ( [Nothing to install over there](./ssh#nothing-to-install-over-there) ): there, errand
must be installed in the image. Apptainer sees it, and needs nothing.
