# Vördr winget source: upgrade the packages in $VordrIds one after the other (set by the agent before this script).
# One JSON object per line on stdout while it runs: "start" before a package, "result" after it with winget's own
# status (PSInstallResultStatus), the installer's exit code and whether a reboot is needed.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
function Out-Vordr($o) { [Console]::Out.WriteLine(($o | ConvertTo-Json -Depth 5 -Compress)) }
# Windows PowerShell's own module folders, as in list.ps1 (LocalSystem has no Documents folder).
$paths = @("$env:ProgramFiles\WindowsPowerShell\Modules", "$env:SystemRoot\system32\WindowsPowerShell\v1.0\Modules")
$docs = [Environment]::GetFolderPath('MyDocuments')
if ($docs) { $paths = @(Join-Path $docs 'WindowsPowerShell\Modules') + $paths }
$env:PSModulePath = $paths -join ';'

$mod =Get-Module -ListAvailable Microsoft.WinGet.Client | Sort-Object Version -Descending | Select-Object -First 1
if (-not $mod) {
    Out-Vordr @{ event = 'client_missing' }
    exit 0
}
Import-Module $mod.Path
foreach ($id in $VordrIds) {
    Out-Vordr @{ event = 'start'; id = $id }
    try {
        $r = Update-WinGetPackage -Id $id -MatchOption Equals -Mode Silent
        $extended = 0
        if ($r.ExtendedErrorCode) { $extended = $r.ExtendedErrorCode.HResult }
        Out-Vordr @{ event = 'result'; id = $id; status = "$($r.Status)"; reboot_required = [bool]$r.RebootRequired
            installer_error_code = [int64]$r.InstallerErrorCode; extended_error_code = [int64]$extended }
    } catch {
        # Update-WinGetPackage throws instead of returning a result when it finds no such package or cannot start.
        Out-Vordr @{ event = 'result'; id = $id; status = 'exception'; error_type = $_.Exception.GetType().FullName
            detail = "$_" }
    }
}
Out-Vordr @{ event = 'done' }
