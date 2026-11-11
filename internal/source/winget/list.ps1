# Vördr winget source: the packages winget would upgrade, as one JSON object on stdout (internal/source/winget).
# Pinned packages do not count (Get-WinGetPackage leaves them out of IsUpdateAvailable, as "winget upgrade" does).
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
function Out-Vordr($o) { [Console]::Out.WriteLine(($o | ConvertTo-Json -Depth 5 -Compress)) }
# Windows PowerShell's own module folders, whoever started the agent: a Windows PowerShell started from PowerShell 7
# inherits 7's module path and does not see modules installed for 5.1. The service runs as
# LocalSystem, which has no Documents folder: GetFolderPath returns "" there, so the user folder is only added if it exists.
$paths = @("$env:ProgramFiles\WindowsPowerShell\Modules", "$env:SystemRoot\system32\WindowsPowerShell\v1.0\Modules")
$docs = [Environment]::GetFolderPath('MyDocuments')
if ($docs) { $paths = @(Join-Path $docs 'WindowsPowerShell\Modules') + $paths }
$env:PSModulePath = $paths -join ';'

$mod =Get-Module -ListAvailable Microsoft.WinGet.Client | Sort-Object Version -Descending | Select-Object -First 1
if (-not $mod) {
    Out-Vordr @{ status = 'client_missing'; user = [Environment]::UserName }
    exit 0
}
try {
    Import-Module $mod.Path
    $version = "$(Get-WinGetVersion)"
    $pending = @(Get-WinGetPackage | Where-Object { $_.IsUpdateAvailable })
} catch {
    # Whatever stops winget here (no App Installer, COM server, catalog): reported as failed; the text is for humans.
    Out-Vordr @{ status = 'failed'; error_type = $_.Exception.GetType().FullName; detail = "$_" }
    exit 0
}
$packages = @($pending | ForEach-Object {
        @{ id = $_.Id; name = $_.Name; installed = "$($_.InstalledVersion)"; available = "$($_.AvailableVersions[0])"
            source = "$($_.Source)" }
    })
Out-Vordr @{ status = 'ok'; winget_version = $version; client_version = "$($mod.Version)"; user = [Environment]::UserName
    packages = $packages }
