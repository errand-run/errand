
    Step 'msvc build tools'
    Invoke-WebRequest -UseBasicParsing 'https://aka.ms/vs/17/release/vs_BuildTools.exe' -OutFile C:\vs_buildtools.exe
    $p = Start-Process C:\vs_buildtools.exe -Wait -PassThru -ArgumentList @(
        '--quiet', '--wait', '--norestart', '--nocache', '--installPath', 'C:\BuildTools',
        '--add', 'Microsoft.VisualStudio.Workload.VCTools', '--includeRecommended' )
    if ( $p.ExitCode -ne 0 -and $p.ExitCode -ne 3010 ) { throw "build tools: exit $( $p.ExitCode )" }
    Remove-Item C:\vs_buildtools.exe