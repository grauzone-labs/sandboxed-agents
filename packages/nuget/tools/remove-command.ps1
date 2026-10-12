#requires -Version 7.0
[CmdletBinding()]
param([string] $InstallDirectory, [switch] $NoPathUpdate)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'command-environment.ps1')
$InstallDirectory = Get-CommandInstallDirectory $InstallDirectory
$pathState = $null
$pathRecord = $null
if (-not $NoPathUpdate) {
    $pathRecord = Get-CommandPathRecord $InstallDirectory
    $pathState = Get-CommandUserPath $InstallDirectory
}
try {
    $command = Join-Path $InstallDirectory 'sandboxed-agents.exe'
    if (Test-Path -LiteralPath $command) { Remove-Item -LiteralPath $command -Force }
    if ($pathState) {
        Set-CommandUserPath $pathState $InstallDirectory $pathRecord -Remove
        if ($pathRecord.Added) { Remove-Item -LiteralPath $pathRecord.File -Force }
    }
    if ((Test-Path -LiteralPath $InstallDirectory -PathType Container) -and @(Get-ChildItem -LiteralPath $InstallDirectory -Force).Count -eq 0) {
        Remove-Item -LiteralPath $InstallDirectory
    }
} finally {
    if ($pathState) { $pathState.Key.Dispose() }
}
