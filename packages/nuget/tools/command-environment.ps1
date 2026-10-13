# Shared implementation for the two PowerShell 7 command scripts.
function Get-CommandInstallDirectory([string] $Directory) {
    if (-not $Directory) {
        if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is required; specify -InstallDirectory.' }
        $Directory = Join-Path $env:LOCALAPPDATA 'Programs/sandboxed-agents'
    }
    return $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Directory)
}

function Get-CommandUserPath([string] $Directory) {
    if ($Directory.Contains(';')) { throw 'An install directory containing a semicolon requires -NoPathUpdate.' }
    $currentUser = Get-Item -LiteralPath 'Registry::HKEY_CURRENT_USER'
    try { $key = $currentUser.CreateSubKey('Environment') } finally { $currentUser.Dispose() }
    try {
        return Read-CommandUserPath $key
    } catch {
        $key.Dispose()
        throw
    }
}

function Read-CommandUserPath($Key) {
    $present = $Key.GetValueNames() -icontains 'Path'
    $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
    if ($present) {
        $kind = $Key.GetValueKind('Path')
        if ($kind -notin @([Microsoft.Win32.RegistryValueKind]::String, [Microsoft.Win32.RegistryValueKind]::ExpandString)) {
            throw 'User PATH must be a registry string or expandable string.'
        }
    }
    $raw = [string] $Key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    return [pscustomobject] @{ Key = $Key; Present = $present; Kind = $kind; Raw = $raw }
}

function Write-CommandNuGet32Debug([string] $Message) {
    # Temporary probes are enabled only by the two fake-registry harness scenarios.
    $scenario = Get-Variable -Name nuget32DiagnosticScenario -Scope Global -ValueOnly -ErrorAction SilentlyContinue
    $fakeRegistry = Get-Variable -Name testUserEnvironment -Scope Global -ValueOnly -ErrorAction SilentlyContinue
    if ($scenario -cin @('path', 'path-aliases') -and $null -ne $fakeRegistry -and $fakeRegistry.GetType().Name -ceq 'TestUserEnvironment') {
        Write-Host "[DEBUG-nuget32] scenario=$scenario $Message"
    }
}

function Test-CommandPathEntry([string] $Entry, [string] $Directory, [Microsoft.Win32.RegistryValueKind] $Kind = [Microsoft.Win32.RegistryValueKind]::String) {
    if (-not $Entry) { return $false }
    # Expandable registry strings expand only for comparison; stored entries are unchanged.
    $expanded = $Entry.Trim().Trim('"')
    if ($Kind -eq [Microsoft.Win32.RegistryValueKind]::ExpandString) {
        $expanded = [System.Environment]::ExpandEnvironmentVariables($expanded)
    }
    $qualified = [System.IO.Path]::IsPathFullyQualified($expanded)
    Write-CommandNuGet32Debug "IsPathFullyQualified=$qualified"
    if (-not $qualified) { return $false }
    try {
        $expanded = [System.IO.Path]::GetFullPath($expanded)
    } catch {
        $exceptionType = $_.Exception.GetType().FullName
        $innerExceptionType = if ($null -eq $_.Exception.InnerException) { '<none>' } else { $_.Exception.InnerException.GetType().FullName }
        Write-CommandNuGet32Debug "GetFullPath exceptionType=$exceptionType innerExceptionType=$innerExceptionType"
        return $false
    }
    Write-CommandNuGet32Debug 'GetFullPath exceptionType=<none>'
    $equal = [string]::Equals($expanded.TrimEnd('\', '/'), $Directory.TrimEnd('\', '/'), [System.StringComparison]::OrdinalIgnoreCase)
    Write-CommandNuGet32Debug "normalizedEquality=$equal"
    return $equal
}

function Get-CommandPathRecord([string] $Directory) {
    $file = Join-Path $Directory '.sandboxed-agents-path-state'
    $record = [pscustomobject] @{ File = $file; Added = $false; Entry = ''; WasEmpty = $false }
    if (Test-Path -LiteralPath $file) {
        try {
            $saved = [System.IO.File]::ReadAllText($file) | ConvertFrom-Json
            $entryType = if ($null -eq $saved.Entry) { '<null>' } else { $saved.Entry.GetType().FullName }
            $wasEmptyType = if ($null -eq $saved.WasEmpty) { '<null>' } else { $saved.WasEmpty.GetType().FullName }
            Write-CommandNuGet32Debug "JSON EntryType=$entryType WasEmptyType=$wasEmptyType"
            if ($saved.Entry -isnot [string] -or $saved.WasEmpty -isnot [bool] -or -not (Test-CommandPathEntry $saved.Entry $Directory)) {
                throw 'Invalid PATH ownership record'
            }
            $record.Added = $true
            $record.Entry = $saved.Entry
            $record.WasEmpty = $saved.WasEmpty
        } catch {
            throw 'The installer PATH state record is invalid; the command was left unchanged.'
        }
    }
    return $record
}

function Set-CommandUserPath($State, [string] $Directory, $Record, [switch] $Remove) {
    # Merge against current registry data after the potentially long executable copy.
    $State = Read-CommandUserPath $State.Key
    $entries = $State.Raw.Split(';')
    $found = $false
    if ($Remove) {
        if (-not $Record.Added) { return }
        $kept = foreach ($entry in $entries) {
            if (-not $found -and $entry -ceq $Record.Entry) { $found = $true } else { $entry }
        }
        if (-not $found) { return }
        $updated = [string]::Join(';', [string[]] @($kept))
    } else {
        foreach ($entry in $entries) {
            if (Test-CommandPathEntry $entry $Directory $State.Kind) { return }
        }
        if (-not $Record.Added) { $Record.WasEmpty = $State.Present -and $State.Raw.Length -eq 0 }
        $Record.Entry = $Directory
        $saved = @{ Entry = $Record.Entry; WasEmpty = $Record.WasEmpty } | ConvertTo-Json -Compress
        [System.IO.File]::WriteAllText($Record.File, $saved)
        $updated = if ($State.Raw.Length -eq 0) { $Directory } else { $State.Raw + ';' + $Directory }
    }
    if ($Remove -and $updated.Length -eq 0 -and -not $Record.WasEmpty) {
        $State.Key.DeleteValue('Path', $false)
    } else {
        $State.Key.SetValue('Path', $updated, $State.Kind)
    }
    Send-CommandEnvironmentChange
}

function Send-CommandEnvironmentChange {
    if (-not $IsWindows) { return }
    if (-not ('SandboxedAgents.EnvironmentNotification' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
namespace SandboxedAgents {
    public static class EnvironmentNotification {
        [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        public static extern IntPtr SendMessageTimeout(
            IntPtr window, uint message, UIntPtr wParam, string lParam,
            uint flags, uint timeout, out UIntPtr result);
    }
}
'@
    }
    $result = [UIntPtr]::Zero
    $null = [SandboxedAgents.EnvironmentNotification]::SendMessageTimeout(
        [IntPtr] 0xffff, 0x001a, [UIntPtr]::Zero, 'Environment', 2, 1000, [ref] $result)
}
