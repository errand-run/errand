if ( -not ( Test-Path 'C:\errand\uv\uv.exe' ) ) {
    $env:UV_UNMANAGED_INSTALL = 'C:\errand\uv'
    Invoke-RestMethod https://astral.sh/uv/install.ps1 | Invoke-Expression
}
$env:UV_PYTHON_INSTALL_DIR = 'C:\errand\uv-python'
& 'C:\errand\uv\uv.exe' python install @V@
if ( $LASTEXITCODE ) { throw "uv python install: exit $LASTEXITCODE" }
$exe = & 'C:\errand\uv\uv.exe' python find --managed-python @V@
if ( $LASTEXITCODE -or -not $exe ) { throw "uv python find: exit $LASTEXITCODE" }
Write-Output "errand-python=$( $exe.Trim() )"
