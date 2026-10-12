#requires -Version 7.0
[CmdletBinding()]
param([string] $InstallDirectory, [switch] $NoPathUpdate)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'command-environment.ps1')
$InstallDirectory = Get-CommandInstallDirectory $InstallDirectory
$source = Join-Path $PSScriptRoot 'sandboxed-agents-windows-amd64.exe'
$checksumLines = @(Get-Content -LiteralPath (Join-Path $PSScriptRoot 'SHA256SUMS') | Where-Object { $_ -match '^[0-9a-fA-F]{64} [ *]sandboxed-agents-windows-amd64\.exe$' })
if ($checksumLines.Count -ne 1) { throw 'SHA256SUMS must contain exactly one valid Windows command checksum.' }
$expected = $checksumLines[0].Substring(0, 64)
if ((Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash -ne $expected) {
    throw 'Windows command checksum does not match SHA256SUMS.'
}
$pathState = $null
$pathRecord = $null
if (-not $NoPathUpdate) {
    $pathRecord = Get-CommandPathRecord $InstallDirectory
    $pathState = Get-CommandUserPath $InstallDirectory
}
$staged = Join-Path $InstallDirectory ('.sandboxed-agents-' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
    Copy-Item -LiteralPath $source -Destination $staged
    if ((Get-FileHash -LiteralPath $staged -Algorithm SHA256).Hash -ne $expected) {
        throw 'Copied command checksum does not match SHA256SUMS.'
    }
    try {
        [System.IO.File]::Move($staged, (Join-Path $InstallDirectory 'sandboxed-agents.exe'), $true)
    } catch {
        throw "Cannot replace sandboxed-agents.exe. If it is running, close it before upgrading and retry. The existing installation was left unchanged. $($_.Exception.Message)"
    }
    if ($pathState) {
        Set-CommandUserPath $pathState $InstallDirectory $pathRecord
    }
} finally {
    if (Test-Path -LiteralPath $staged) { Remove-Item -LiteralPath $staged -Force }
    if ($pathState) { $pathState.Key.Dispose() }
}
