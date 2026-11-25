
    Step 'python @V@'
    Invoke-WebRequest -UseBasicParsing 'https://www.python.org/ftp/python/@V@/python-@V@-amd64.exe' -OutFile C:\python-setup.exe
    $p = Start-Process C:\python-setup.exe -Wait -PassThru -ArgumentList @(
        '/quiet', 'InstallAllUsers=1', 'PrependPath=1', 'Include_test=0' )
    if ( $p.ExitCode -ne 0 ) { throw "python: exit $( $p.ExitCode )" }
    Remove-Item C:\python-setup.exe