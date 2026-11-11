# Vördr Windows Update source: download and install the updates in $VordrIds (update ids, set by the agent before
# this script), one after the other. One JSON object per line on stdout while it runs: "download" and "start" before
# an update is downloaded or installed, "result" after it with Windows Update's own OperationResultCode (2 succeeded,
# 3 succeeded with errors, 4 failed, 5 aborted), its HRESULT and whether a reboot is needed.
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
function Out-Thrown($id, $stage, $err) {
    Out-Vordr @{ event = 'result'; id = $id; stage = $stage; status = 'exception'; hresult = Get-HResult $err
        error_type = $err.Exception.GetType().FullName; detail = "$err" }
}

try {
    # The Windows Update Agent answers only through COM, and COM reports a refusal (service disabled, no network,
    # update server unreachable, another installation) only by throwing. Its HRESULT is the fact.
    $session = New-Object -ComObject Microsoft.Update.Session
    $session.ClientApplicationID = 'Vordr'
    $found = $session.CreateUpdateSearcher().Search("IsInstalled=0 and IsHidden=0 and Type='Software'")
    $installer = $session.CreateUpdateInstaller()
} catch {
    Out-Vordr @{ event = 'search_failed'; hresult = Get-HResult $_; error_type = $_.Exception.GetType().FullName
        detail = "$_" }
    exit 0
}
if ($installer.IsBusy) {
    Out-Vordr @{ event = 'busy' }
    exit 0
}
$installer.AllowSourcePrompts = $false # nobody is there to insert a medium

# The plan as Windows Update sees it now. An update it no longer offers was installed meanwhile (by Windows itself);
# one that waits for a reboot is installed too. Neither is installed again.
$byId = @{}
foreach ($u in $found.Updates) { $byId["$($u.Identity.UpdateID)"] = $u }
$plan = New-Object System.Collections.ArrayList
foreach ($id in $VordrIds) {
    $u = $byId[$id]
    if ($null -eq $u -or $u.RebootRequired) {
        Out-Vordr @{ event = 'result'; id = $id; status = 'not_pending'; reboot_required = ($null -ne $u) }
        continue
    }
    [void]$plan.Add($u)
}

$ready = New-Object System.Collections.ArrayList
foreach ($u in $plan) {
    $id = "$($u.Identity.UpdateID)"
    Out-Vordr @{ event = 'download'; id = $id }
    try {
        if (-not $u.EulaAccepted) {
            # The install button is the operator's consent, as the click in Settings is (ADR-0086).
            $u.AcceptEula()
            Out-Vordr @{ event = 'eula_accepted'; id = $id }
        }
        $one = New-Object -ComObject Microsoft.Update.UpdateColl
        [void]$one.Add($u)
        $downloader = $session.CreateUpdateDownloader()
        $downloader.Updates = $one
        $r = $downloader.Download().GetUpdateResult(0)
    } catch {
        Out-Thrown $id 'download' $_ # COM reports a refusal only by throwing (see above)
        continue
    }
    $code = [int]$r.ResultCode
    if ($code -ne 2 -and $code -ne 3) {
        Out-Vordr @{ event = 'result'; id = $id; stage = 'download'; result_code = $code; hresult = [int64]$r.HResult }
        continue
    }
    [void]$ready.Add($u)
}

foreach ($u in $ready) {
    $id = "$($u.Identity.UpdateID)"
    Out-Vordr @{ event = 'start'; id = $id }
    try {
        $one = New-Object -ComObject Microsoft.Update.UpdateColl
        [void]$one.Add($u)
        $installer.Updates = $one
        $r = $installer.Install().GetUpdateResult(0)
    } catch {
        Out-Thrown $id 'install' $_ # COM reports a refusal only by throwing (see above)
        continue
    }
    Out-Vordr @{ event = 'result'; id = $id; stage = 'install'; result_code = [int]$r.ResultCode
        hresult = [int64]$r.HResult; reboot_required = [bool]$r.RebootRequired }
}
Out-Vordr @{ event = 'done' }
