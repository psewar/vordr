# Vördr Windows Update source: the software updates Windows Update offers, as one JSON object on stdout
# (internal/source/winupdate). It only searches; it downloads and installs nothing. The agent decides which of them
# count (feature upgrades, optional updates and updates waiting for a reboot do not); this script reports them all.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
function Out-Vordr($o) { [Console]::Out.WriteLine(($o | ConvertTo-Json -Depth 5 -Compress)) }
# The HRESULT Windows Update gave: PowerShell wraps the COMException of a failed call in an exception of its own.
function Get-HResult($err) {
    $e = $err.Exception
    while ($e.InnerException -and -not ($e -is [System.Runtime.InteropServices.COMException])) { $e = $e.InnerException }
    [int64]$e.HResult
}

try {
    # The Windows Update Agent answers only through COM, and COM reports a refusal (service disabled, no network,
    # update server unreachable) only by throwing. Its HRESULT is the fact; the text is for humans.
    $session = New-Object -ComObject Microsoft.Update.Session
    $session.ClientApplicationID = 'Vordr'
    $found = $session.CreateUpdateSearcher().Search("IsInstalled=0 and IsHidden=0 and Type='Software'")
    $busy = [bool]$session.CreateUpdateInstaller().IsBusy
    $agent = "$((New-Object -ComObject Microsoft.Update.AgentInfo).GetInfo('ProductVersionString'))"
} catch {
    Out-Vordr @{ status = 'failed'; hresult = Get-HResult $_; error_type = $_.Exception.GetType().FullName
        detail = "$_"; user = [Environment]::UserName }
    exit 0
}
$updates = @(foreach ($u in $found.Updates) {
        @{ id = "$($u.Identity.UpdateID)"; revision = [int64]$u.Identity.RevisionNumber; title = "$($u.Title)"
            kb = @($u.KBArticleIDs | ForEach-Object { "$_" })
            categories = @($u.Categories | ForEach-Object { "$($_.CategoryID)" })
            browse_only = [bool]$u.BrowseOnly; reboot_required = [bool]$u.RebootRequired
            eula_accepted = [bool]$u.EulaAccepted; downloaded = [bool]$u.IsDownloaded
            max_download_size = [int64]$u.MaxDownloadSize }
    })
Out-Vordr @{ status = 'ok'; agent_version = $agent; user = [Environment]::UserName; installer_busy = $busy
    updates = $updates }
