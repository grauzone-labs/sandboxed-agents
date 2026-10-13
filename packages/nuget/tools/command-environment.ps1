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

function Resolve-CommandPathForComparison([string] $Path) {
    $Path = [System.IO.Path]::GetFullPath($Path)
    if (-not $IsWindows) { return $Path }
    # Windows PATH entries may use 8.3 names even when the install directory uses long names.
    if (-not ('SandboxedAgents.PathCanonicalization' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;
namespace SandboxedAgents {
    public static class PathCanonicalization {
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        public static extern uint GetLongPathName(string shortPath, StringBuilder longPath, uint bufferLength);
    }
}
'@
    }
    $suffix = [System.Collections.Generic.List[string]]::new()
    $existing = $Path
    while (-not [System.IO.Directory]::Exists($existing)) {
        $parent = [System.IO.Path]::GetDirectoryName($existing)
        if (-not $parent -or $parent -ceq $existing) { return $Path }
        $suffix.Insert(0, [System.IO.Path]::GetFileName($existing))
        $existing = $parent
    }
    $capacity = 260
    while ($capacity -le 32768) {
        $buffer = [System.Text.StringBuilder]::new($capacity)
        $length = [SandboxedAgents.PathCanonicalization]::GetLongPathName($existing, $buffer, [uint32] $capacity)
        if ($length -eq 0) { break }
        if ($length -lt $capacity) {
            $canonical = $buffer.ToString()
            foreach ($segment in $suffix) { $canonical = Join-Path $canonical $segment }
            return $canonical
        }
        $capacity = [int] $length + 1
    }
    return $Path
}

function Test-CommandPathEntry([string] $Entry, [string] $Directory, [Microsoft.Win32.RegistryValueKind] $Kind = [Microsoft.Win32.RegistryValueKind]::String) {
    if (-not $Entry) { return $false }
    # Expandable registry strings expand only for comparison; stored entries are unchanged.
    $expanded = $Entry.Trim().Trim('"')
    if ($Kind -eq [Microsoft.Win32.RegistryValueKind]::ExpandString) {
        $expanded = [System.Environment]::ExpandEnvironmentVariables($expanded)
    }
    if (-not [System.IO.Path]::IsPathFullyQualified($expanded)) { return $false }
    try {
        $expanded = Resolve-CommandPathForComparison $expanded
        $Directory = Resolve-CommandPathForComparison $Directory
    } catch {
        return $false
    }
    return [string]::Equals($expanded.TrimEnd('\', '/'), $Directory.TrimEnd('\', '/'), [System.StringComparison]::OrdinalIgnoreCase)
}

function Get-CommandPathRecord([string] $Directory) {
    $file = Join-Path $Directory '.sandboxed-agents-path-state'
    $record = [pscustomobject] @{ File = $file; Added = $false; Entry = ''; WasEmpty = $false }
    if (Test-Path -LiteralPath $file) {
        try {
            $saved = [System.IO.File]::ReadAllText($file) | ConvertFrom-Json
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
        try {
            $State.Key.SetValue('Path', $updated, $State.Kind)
        } catch {
            if (-not $Record.Added) {
                Remove-Item -LiteralPath $Record.File -Force -ErrorAction SilentlyContinue
            }
            throw
        }
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
