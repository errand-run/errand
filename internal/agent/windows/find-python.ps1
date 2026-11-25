
foreach ( $c in @( @( 'py', '-3' ), @( 'python' ), @( 'python3' ) ) ) {
    $exe = Get-Command $c[ 0 ] -ErrorAction SilentlyContinue
    if ( -not $exe -or $exe.Source -like '*WindowsApps*' ) { continue }     # the Store's stub: opens the Store
    $args_ = @( $c | Select-Object -Skip 1 ) + @( '-c', 'import sys; print( sys.executable )' )
    $got = & $exe.Source @args_ 2>$null
    if ( $LASTEXITCODE -eq 0 -and $got ) { Write-Output "errand-python=$( ( $got | Select-Object -Last 1 ).Trim() )"; exit 0 }
}
exit 3
