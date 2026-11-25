$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Start-Transcript -Path C:\errand-setup.log
$media = Split-Path -Parent $MyInvocation.MyCommand.Path
function Step( $what ) { Write-Host "== $what" }
New-Item -ItemType Directory -Force C:\errand | Out-Null
try {
    Step 'sshd'
    if ( -not ( Get-Service sshd -ErrorAction SilentlyContinue ) ) {
        Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0 | Out-Null
    }
    Set-Service sshd -StartupType Automatic
    Start-Service sshd
    $keys = 'C:\ProgramData\ssh\administrators_authorized_keys'
    Copy-Item ( Join-Path $media 'errand_key.pub' ) $keys
    icacls $keys /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F' | Out-Null
    $config = 'C:\ProgramData\ssh\sshd_config'
    ( Get-Content $config ) -replace '^#?PasswordAuthentication .*', 'PasswordAuthentication no' | Set-Content $config
    Restart-Service sshd
    New-NetFirewallRule -Name errand-sshd -DisplayName 'errand sshd' -Profile Any -Direction Inbound `
        -Protocol TCP -LocalPort 22 -Action Allow -ErrorAction SilentlyContinue | Out-Null

    # A machine that lives for minutes has no use for updates, and the
    # scanning of every file a compiler writes halves its speed.
    Step 'quiet'
    Stop-Service wuauserv -ErrorAction SilentlyContinue
    Set-Service wuauserv -StartupType Disabled -ErrorAction SilentlyContinue
    try { Set-MpPreference -DisableRealtimeMonitoring $true -ExclusionPath C:\errand, C:\BuildTools } catch { }

@STEPS@
    Set-Content C:\errand\setup.status 'ok'
} catch {
    Set-Content C:\errand\setup.status "failed: $_"
} finally {
    Stop-Transcript
    Stop-Computer -Force
}
