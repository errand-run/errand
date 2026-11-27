"""`errand/_version.py`, written when errand-lib is built into a wheel -- never for an editable install.

An editable install builds in the sources themselves: a version file written there is a file the build changed in a copy errand
keeps on another machine ( `develop = [ "errand/lib" ]` over an environment ), and the next push stops on it. Editable, the
package says "0+source" ( `errand/__init__.py` ).
"""
from hatchling.builders.hooks.plugin.interface import BuildHookInterface


class VersionFile( BuildHookInterface ):
    PLUGIN_NAME = "custom"

    def initialize( self, version, build_data ):
        if version == "editable":
            return
        path = f"{ self.root }/errand/_version.py"
        text = f"__version__ = { self.metadata.version!r}\n"
        try:
            with open( path ) as f:
                if f.read( ) == text:
                    return              # as it is: not even its date changes
        except OSError:
            pass
        with open( path, "w" ) as f:
            f.write( text )
