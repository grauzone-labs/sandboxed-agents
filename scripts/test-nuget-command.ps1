#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $Scenario,
    [Parameter(Mandatory)][string] $Repository,
    [Parameter(Mandatory)][string] $TestDirectory
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($Scenario -eq 'windows-registry' -and $env:SANDBOXED_AGENTS_TEST_HKCU -cne '1') {
    throw 'Real user PATH registry integration requires SANDBOXED_AGENTS_TEST_HKCU=1.'
}

function Assert-Equal($Actual, $Expected, [string] $Message) {
    if ($Actual -cne $Expected) { throw "$Message`: expected '$Expected', got '$Actual'" }
}

class TestUserEnvironment {
    [string] $Value
    [Microsoft.Win32.RegistryValueKind] $Kind
    [bool] $Present = $true
    [int] $Writes = 0
    [object] CreateSubKey([string] $Name) {
        if ($Name -cne 'Environment') { throw 'Installer opened an unrelated registry key' }
        return $this
    }
    [string[]] GetValueNames() {
        if ($this.Present) { return @('Path') }
        return @()
    }
    [object] GetValue([string] $Name, [object] $Default, [Microsoft.Win32.RegistryValueOptions] $Options) {
        if ($Name -ine 'Path' -or $Options -ne [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) { throw 'Installer did not read raw user PATH' }
        if ($this.Present) { return $this.Value }
        return $Default
    }
    [Microsoft.Win32.RegistryValueKind] GetValueKind([string] $Name) { return $this.Kind }
    [void] SetValue([string] $Name, [object] $Value, [Microsoft.Win32.RegistryValueKind] $Kind) {
        if ($Name -ine 'Path') { throw 'Installer changed an unrelated registry value' }
        $this.Value = $Value
        $this.Kind = $Kind
        $this.Present = $true
        $this.Writes++
    }
    [void] DeleteValue([string] $Name, [bool] $ThrowOnMissingValue) {
        if ($Name -ine 'Path') { throw 'Installer deleted an unrelated registry value' }
        if ($ThrowOnMissingValue -and -not $this.Present) { throw 'Missing user PATH' }
        $this.Value = ''
        $this.Present = $false
        $this.Writes++
    }
    [void] Dispose() {}
}

function Use-TestUserEnvironment {
    $global:testUserEnvironment = [TestUserEnvironment]::new()
    function global:Get-Item {
        param([string] $LiteralPath)
        if ($LiteralPath -cne 'Registry::HKEY_CURRENT_USER') { throw 'Installer read an unrelated registry key' }
        return $global:testUserEnvironment
    }
}

function Write-CommandChecksums([string] $Binary, [string] $ChecksumFile) {
    $hash = (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText($ChecksumFile, "$hash  sandboxed-agents-windows-amd64.exe`n")
    return $hash
}

$package = Join-Path $TestDirectory 'package'
$tools = Join-Path $package 'tools'
$install = Join-Path $TestDirectory 'command with spaces'
New-Item -ItemType Directory -Path $tools | Out-Null
Copy-Item -Path (Join-Path $Repository 'packages/nuget/tools/*') -Destination $tools
$binary = Join-Path $tools 'sandboxed-agents-windows-amd64.exe'
[System.IO.File]::WriteAllText($binary, 'first release command')
$hash = Write-CommandChecksums $binary (Join-Path $tools 'SHA256SUMS')
$installer = Join-Path $tools 'install-command.ps1'
$remover = Join-Path $tools 'remove-command.ps1'

switch ($Scenario) {
    'path-registry-kind' {
        $env:LOCALAPPDATA = Join-Path $TestDirectory 'local app data'
        $install = Join-Path $env:LOCALAPPDATA 'Programs/sandboxed-agents'
        $literal = Join-Path '%LOCALAPPDATA%' 'Programs/sandboxed-agents'
        foreach ($kind in @([Microsoft.Win32.RegistryValueKind]::String, [Microsoft.Win32.RegistryValueKind]::ExpandString)) {
            Use-TestUserEnvironment
            $global:testUserEnvironment.Kind = $kind
            $global:testUserEnvironment.Value = $literal
            & $installer
            $expected = if ($kind -eq [Microsoft.Win32.RegistryValueKind]::String) { $literal + ';' + $install } else { $literal }
            Assert-Equal $global:testUserEnvironment.Value $expected 'Comparison follows user PATH registry expansion semantics'
            & $installer
            Assert-Equal $global:testUserEnvironment.Value $expected 'Upgrade retains the registry-kind-dependent PATH entry'
            & $remover
            Assert-Equal $global:testUserEnvironment.Value $literal 'Removal preserves the original unexpanded user PATH text'
            Assert-Equal $global:testUserEnvironment.Kind $kind 'Removal preserves original PATH registry type'
            if (Test-Path -LiteralPath $install) { throw 'Removal left installer state behind after registry-kind comparison' }
        }
    }
    'replacement-failure' {
        $destination = Join-Path $install 'sandboxed-agents.exe'
        New-Item -ItemType Directory -Path $destination -Force | Out-Null
        $previous = Join-Path $destination 'keep.txt'
        [System.IO.File]::WriteAllText($previous, 'previous installation content')
        $failure = $null
        try { & $installer -InstallDirectory $install -NoPathUpdate } catch { $failure = $_ }
        if (-not $failure -or $failure.ToString() -notmatch 'close it before upgrading') { throw 'Replacement failure did not explain running-command upgrade locks' }
        Assert-Equal ([System.IO.File]::ReadAllText($previous)) 'previous installation content' 'Replacement failure preserves prior installation'
        Assert-Equal @(Get-ChildItem -LiteralPath $install -Force).Count 1 'Failed replacement staging file cleaned up'
    }
    'locked-upgrade' {
        if (-not $IsWindows) { throw 'Sharing-lock integration requires Windows' }
        & $installer -InstallDirectory $install -NoPathUpdate
        $destination = Join-Path $install 'sandboxed-agents.exe'
        [System.IO.File]::WriteAllText($binary, 'second release command')
        $null = Write-CommandChecksums $binary (Join-Path $tools 'SHA256SUMS')
        $locked = [System.IO.File]::Open($destination, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::None)
        $failure = $null
        try {
            try { & $installer -InstallDirectory $install -NoPathUpdate } catch { $failure = $_ }
        } finally { $locked.Dispose() }
        if (-not $failure -or $failure.ToString() -notmatch 'close it before upgrading') { throw 'Locked executable upgrade did not give close-and-retry guidance' }
        Assert-Equal ([System.IO.File]::ReadAllText($destination)) 'first release command' 'Locked upgrade preserves previous executable bytes'
        Assert-Equal @(Get-ChildItem -LiteralPath $install -Force).Count 1 'Locked upgrade staging file cleaned up'
    }
    'path-during-copy' {
        Use-TestUserEnvironment
        $global:testUserEnvironment.Kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        $global:testUserEnvironment.Value = '%ORIGINAL_TEST_VARIABLE%/bin'
        function Copy-Item {
            param([string] $LiteralPath, [string] $Destination)
            Microsoft.PowerShell.Management\Copy-Item -LiteralPath $LiteralPath -Destination $Destination
            $global:testUserEnvironment.Value = '%ORIGINAL_TEST_VARIABLE%/bin;%DURING_COPY_TEST_VARIABLE%/bin'
            $global:testUserEnvironment.Kind = [Microsoft.Win32.RegistryValueKind]::String
        }
        & $installer -InstallDirectory $install
        $expected = '%ORIGINAL_TEST_VARIABLE%/bin;%DURING_COPY_TEST_VARIABLE%/bin'
        Assert-Equal $global:testUserEnvironment.Value ($expected + ';' + $install) 'Install preserves unrelated PATH edits made during the copy'
        Assert-Equal $global:testUserEnvironment.Kind ([Microsoft.Win32.RegistryValueKind]::String) 'Install preserves registry type changed during the copy'
        & $remover -InstallDirectory $install
        Assert-Equal $global:testUserEnvironment.Value $expected 'Removal preserves unrelated entries added during installation'
    }
    'owned-path' {
        Use-TestUserEnvironment
        $global:testUserEnvironment.Kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        $env:SANDBOXED_AGENTS_TEST_INSTALL = $install
        $raw = '%UNEXPANDED_TEST_VARIABLE%/bin'
        $global:testUserEnvironment.Value = $raw
        & $installer -InstallDirectory $install
        $global:testUserEnvironment.Value += ';%SANDBOXED_AGENTS_TEST_INSTALL%;' + $install
        & $installer -InstallDirectory $install
        & $remover -InstallDirectory $install
        Assert-Equal $global:testUserEnvironment.Value ($raw + ';%SANDBOXED_AGENTS_TEST_INSTALL%;' + $install) 'Removal deletes only the recorded PATH entry and preserves later user entries'
    }
    'preexisting-path' {
        $env:SANDBOXED_AGENTS_TEST_INSTALL = $install
        foreach ($entry in @($install, '%SANDBOXED_AGENTS_TEST_INSTALL%')) {
            Use-TestUserEnvironment
            $global:testUserEnvironment.Kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
            $raw = '%UNEXPANDED_TEST_VARIABLE%/bin;' + $entry + ';'
            $global:testUserEnvironment.Value = $raw
            & $installer -InstallDirectory $install
            & $installer -InstallDirectory $install
            Assert-Equal $global:testUserEnvironment.Value $raw 'Installer leaves pre-existing matching PATH entries unchanged'
            & $remover -InstallDirectory $install -NoPathUpdate
            & $remover -InstallDirectory $install
            Assert-Equal $global:testUserEnvironment.Value $raw 'Removal preserves a pre-existing matching PATH entry'
            Assert-Equal $global:testUserEnvironment.Writes 0 'Pre-existing PATH entry is never rewritten'
            if (Test-Path -LiteralPath $install) { throw 'Unowned PATH entry left an installer record behind' }
        }
    }
    'empty-path' {
        Use-TestUserEnvironment
        foreach ($present in @($false, $true)) {
            foreach ($kind in @([Microsoft.Win32.RegistryValueKind]::String, [Microsoft.Win32.RegistryValueKind]::ExpandString)) {
                $global:testUserEnvironment.Present = $present
                $global:testUserEnvironment.Value = ''
                $global:testUserEnvironment.Kind = $kind
                & $installer -InstallDirectory $install
                Assert-Equal $global:testUserEnvironment.Present $true 'Installing creates user PATH'
                Assert-Equal $global:testUserEnvironment.Value $install 'Initially empty user PATH gains install directory'
                & $installer -InstallDirectory $install
                & $installer -InstallDirectory $install -NoPathUpdate
                & $remover -InstallDirectory $install -NoPathUpdate
                Assert-Equal $global:testUserEnvironment.Value $install '-NoPathUpdate leaves the existing user PATH entry intact'
                & $remover -InstallDirectory $install
                Assert-Equal $global:testUserEnvironment.Present $present 'Removal restores absent versus present-empty user PATH'
                if ($present) {
                    Assert-Equal $global:testUserEnvironment.Value '' 'Removal restores an empty user PATH value'
                    Assert-Equal $global:testUserEnvironment.Kind $kind 'Removal preserves original empty PATH registry type'
                }
                if (Test-Path -LiteralPath $install) { throw 'Removal left installer state behind in the install directory' }
            }
        }
    }
    'relative-path' {
        Use-TestUserEnvironment
        $global:testUserEnvironment.Kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        New-Item -ItemType Directory -Path $install | Out-Null
        [System.IO.File]::WriteAllText((Join-Path $install 'keep.txt'), 'keep directory for current-location test')
        $originalDirectory = [System.Environment]::CurrentDirectory
        $originalLocation = Get-Location
        try {
            foreach ($relative in @('command with spaces', '.')) {
                $workingDirectory = if ($relative -eq '.') { $install } else { $TestDirectory }
                Set-Location -LiteralPath $workingDirectory
                [System.Environment]::CurrentDirectory = $workingDirectory
                $global:testUserEnvironment.Value = $relative
                & $installer -InstallDirectory $install
                Assert-Equal $global:testUserEnvironment.Value ($relative + ';' + $install) 'Relative PATH entry is preserved and absolute install directory appended'
                & $remover -InstallDirectory $install
                Assert-Equal $global:testUserEnvironment.Value $relative 'Removal preserves relative PATH entry'
            }
        } finally {
            Set-Location -LiteralPath $originalLocation.Path
            [System.Environment]::CurrentDirectory = $originalDirectory
        }
    }
    'path-errors' {
        Use-TestUserEnvironment
        $global:testUserEnvironment.Kind = [Microsoft.Win32.RegistryValueKind]::DWord
        $global:testUserEnvironment.Value = '17'
        New-Item -ItemType Directory -Path $install | Out-Null
        [System.IO.File]::WriteAllText((Join-Path $install 'sandboxed-agents.exe'), 'previous command')
        $failure = $null
        try { & $installer -InstallDirectory $install } catch { $failure = $_ }
        if (-not $failure -or $failure.ToString() -notmatch 'User PATH must be a registry string') { throw 'Installer accepted a non-string user PATH' }
        Assert-Equal ([System.IO.File]::ReadAllText((Join-Path $install 'sandboxed-agents.exe'))) 'previous command' 'Invalid PATH preserves installed command'
        Assert-Equal $global:testUserEnvironment.Writes 0 'Invalid PATH is not rewritten'
        $install = Join-Path $TestDirectory 'directory;with-semicolon'
        $failure = $null
        try { & $installer -InstallDirectory $install } catch { $failure = $_ }
        if (-not $failure -or $failure.ToString() -notmatch 'semicolon requires -NoPathUpdate') { throw 'Installer accepted an ambiguous PATH directory' }
        if (Test-Path -LiteralPath $install) { throw 'Invalid PATH directory was created before refusal' }
        & $installer -InstallDirectory $install -NoPathUpdate
        & $remover -InstallDirectory $install -NoPathUpdate
    }
    'default-no-path' {
        function Get-Item { throw '-NoPathUpdate accessed the registry' }
        $env:LOCALAPPDATA = Join-Path $TestDirectory 'local app data'
        $expectedDirectory = Join-Path $env:LOCALAPPDATA 'Programs/sandboxed-agents'
        & $installer -NoPathUpdate
        Assert-Equal ([System.IO.File]::ReadAllText((Join-Path $expectedDirectory 'sandboxed-agents.exe'))) 'first release command' 'Default per-user install directory'
        & $remover -NoPathUpdate
        if (Test-Path -LiteralPath $expectedDirectory) { throw 'Default removal left the command directory behind' }
    }
    'path-aliases' {
        Use-TestUserEnvironment
        $global:testUserEnvironment.Kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        $env:LOCALAPPDATA = Join-Path $TestDirectory 'local app data'
        $install = Join-Path $env:LOCALAPPDATA 'Programs/sandboxed-agents'
        $alias = '%LOCALAPPDATA%/Programs/sandboxed-agents'
        $raw = '%UNEXPANDED_TEST_VARIABLE%/bin;;  another directory  '
        $original = $raw + ';' + $alias + ';"' + $install.ToUpperInvariant() + '/"'
        $global:testUserEnvironment.Value = $original
        & $installer
        Assert-Equal $global:testUserEnvironment.Writes 0 'Equivalent PATH entries are not duplicated or retyped'
        & $remover
        Assert-Equal $global:testUserEnvironment.Value $original 'Removal preserves every pre-existing equivalent PATH entry'
        $global:testUserEnvironment.Present = $false
        $global:testUserEnvironment.Value = ''
        & $remover
        Assert-Equal $global:testUserEnvironment.Present $false 'Removing absent command leaves absent PATH absent'
        & $installer
        Assert-Equal $global:testUserEnvironment.Value $install 'Absent user PATH created with install directory'
        Assert-Equal $global:testUserEnvironment.Kind ([Microsoft.Win32.RegistryValueKind]::ExpandString) 'New PATH is expandable'
        & $remover
        Assert-Equal $global:testUserEnvironment.Present $false 'Install and removal restore absent user PATH'
    }
    'windows-registry' {
        if (-not $IsWindows) { throw 'Real registry integration requires Windows' }
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
        $wasPresent = $key.GetValueNames() -icontains 'Path'
        $originalValue = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $originalKind = if ($wasPresent) { $key.GetValueKind('Path') } else { [Microsoft.Win32.RegistryValueKind]::ExpandString }
        try {
            foreach ($kind in @([Microsoft.Win32.RegistryValueKind]::ExpandString, [Microsoft.Win32.RegistryValueKind]::String)) {
                $raw = '%LOCALAPPDATA%\unrelated-command;;C:\unchanged directory;'
                $key.SetValue('Path', $raw, $kind)
                & $installer -InstallDirectory $install
                Assert-Equal $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) ($raw + ';' + $install) 'Actual raw registry PATH installed'
                Assert-Equal $key.GetValueKind('Path') $kind 'Actual registry value type preserved'
                & $installer -InstallDirectory $install
                Assert-Equal $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) ($raw + ';' + $install) 'Actual registry upgrade is idempotent'
                & $remover -InstallDirectory $install
                Assert-Equal $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) $raw 'Actual registry unrelated entries preserved on removal'
                & $installer -InstallDirectory $install -NoPathUpdate
                & $remover -InstallDirectory $install -NoPathUpdate
                Assert-Equal $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) $raw '-NoPathUpdate leaves actual user PATH untouched'
            }
            $key.DeleteValue('Path', $false)
            & $installer -InstallDirectory $install
            & $remover -InstallDirectory $install
            Assert-Equal ($key.GetValueNames() -icontains 'Path') $false 'Actual absent user PATH restored after removal'
            foreach ($kind in @([Microsoft.Win32.RegistryValueKind]::String, [Microsoft.Win32.RegistryValueKind]::ExpandString)) {
                $key.SetValue('Path', '', $kind)
                & $installer -InstallDirectory $install
                & $installer -InstallDirectory $install
                & $remover -InstallDirectory $install
                Assert-Equal ($key.GetValueNames() -icontains 'Path') $true 'Actual present-empty user PATH remains present after removal'
                Assert-Equal $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) '' 'Actual empty user PATH value restored'
                Assert-Equal $key.GetValueKind('Path') $kind 'Actual empty user PATH registry type restored'
            }
            $env:SANDBOXED_AGENTS_TEST_INSTALL = $install
            foreach ($entry in @($install, '%SANDBOXED_AGENTS_TEST_INSTALL%')) {
                $raw = '%LOCALAPPDATA%\unrelated-command;' + $entry
                $key.SetValue('Path', $raw, [Microsoft.Win32.RegistryValueKind]::ExpandString)
                & $installer -InstallDirectory $install
                & $installer -InstallDirectory $install
                & $remover -InstallDirectory $install
                Assert-Equal $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) $raw 'Actual pre-existing matching PATH entry preserved after removal'
            }
        } finally {
            if ($wasPresent) { $key.SetValue('Path', $originalValue, $originalKind) } else { $key.DeleteValue('Path', $false) }
            $key.Dispose()
            . (Join-Path $tools 'command-environment.ps1')
            Send-CommandEnvironmentChange
        }
    }
    'path' {
        Use-TestUserEnvironment
        $raw = '%UNEXPANDED_TEST_VARIABLE%/bin;;  another directory  ;"quoted path";'
        foreach ($kind in @([Microsoft.Win32.RegistryValueKind]::ExpandString, [Microsoft.Win32.RegistryValueKind]::String)) {
            $global:testUserEnvironment.Value = $raw
            $global:testUserEnvironment.Kind = $kind
            & $installer -InstallDirectory $install
            Assert-Equal $global:testUserEnvironment.Value ($raw + ';' + $install) 'User PATH appended without changing original text'
            Assert-Equal $global:testUserEnvironment.Kind $kind 'User PATH registry type preserved'
            $writes = $global:testUserEnvironment.Writes
            & $installer -InstallDirectory $install
            Assert-Equal $global:testUserEnvironment.Writes $writes 'Upgrade does not duplicate PATH entry'
            & $remover -InstallDirectory $install
            Assert-Equal $global:testUserEnvironment.Value $raw 'Removal preserves every unrelated PATH entry'
            Assert-Equal $global:testUserEnvironment.Kind $kind 'Removal preserves registry type'
        }
    }
    'remove' {
        & $installer -InstallDirectory $install -NoPathUpdate
        $unrelated = Join-Path $install 'keep.txt'
        [System.IO.File]::WriteAllText($unrelated, 'unrelated file')
        & $remover -InstallDirectory $install -NoPathUpdate
        if (Test-Path -LiteralPath (Join-Path $install 'sandboxed-agents.exe')) { throw 'Remover did not remove the command' }
        Assert-Equal ([System.IO.File]::ReadAllText($unrelated)) 'unrelated file' 'Removal preserves unrelated files'
        & $remover -InstallDirectory $install -NoPathUpdate
        Remove-Item -LiteralPath $unrelated
        & $remover -InstallDirectory $install -NoPathUpdate
        if (Test-Path -LiteralPath $install) { throw 'Remover did not remove an empty install directory' }
        & $remover -InstallDirectory $install -NoPathUpdate
    }
    'copied-checksum' {
        New-Item -ItemType Directory -Path $install | Out-Null
        $destination = Join-Path $install 'sandboxed-agents.exe'
        [System.IO.File]::WriteAllText($destination, 'previous command')
        # Simulate a corrupt copy at the filesystem boundary, keeping the installer and hash checks real.
        function Copy-Item {
            param([string] $LiteralPath, [string] $Destination)
            Microsoft.PowerShell.Management\Copy-Item -LiteralPath $LiteralPath -Destination $Destination
            [System.IO.File]::WriteAllText($Destination, 'corrupt copied command')
        }
        $failure = $null
        try { & $installer -InstallDirectory $install -NoPathUpdate } catch { $failure = $_ }
        if (-not $failure -or $failure.ToString() -notmatch 'Copied command checksum') { throw 'Installer did not detect a corrupt copied command' }
        Assert-Equal ([System.IO.File]::ReadAllText($destination)) 'previous command' 'Corrupt copy preserves installed command'
        Assert-Equal @(Get-ChildItem -LiteralPath $install -Force).Count 1 'Staging files cleaned up'
    }
    'checksum' {
        New-Item -ItemType Directory -Path $install | Out-Null
        $destination = Join-Path $install 'sandboxed-agents.exe'
        foreach ($sums in @(
            ('0' * 64 + "  sandboxed-agents-windows-amd64.exe`n"),
            "$hash  another-command.exe`n",
            "$hash  sandboxed-agents-windows-amd64.exe`n$hash  sandboxed-agents-windows-amd64.exe`n",
            "not-a-hash  sandboxed-agents-windows-amd64.exe`n"
        )) {
            [System.IO.File]::WriteAllText($destination, 'previous command')
            [System.IO.File]::WriteAllText((Join-Path $tools 'SHA256SUMS'), $sums)
            $failure = $null
            try { & $installer -InstallDirectory $install -NoPathUpdate } catch { $failure = $_ }
            if (-not $failure) { throw 'Installer accepted invalid checksums' }
            Assert-Equal ([System.IO.File]::ReadAllText($destination)) 'previous command' 'Refused upgrade preserves installed command'
        }
    }
    'upgrade' {
        & $installer -InstallDirectory $install -NoPathUpdate
        [System.IO.File]::WriteAllText($binary, 'second release command')
        $null = Write-CommandChecksums $binary (Join-Path $tools 'SHA256SUMS')
        & $installer -InstallDirectory $install -NoPathUpdate
        Assert-Equal ([System.IO.File]::ReadAllText((Join-Path $install 'sandboxed-agents.exe'))) 'second release command' 'Upgraded command bytes'
    }
    'install' {
        & $installer -InstallDirectory $install -NoPathUpdate
        $destination = Join-Path $install 'sandboxed-agents.exe'
        if (-not (Test-Path -LiteralPath $destination)) { throw 'Installer did not install the command' }
        Assert-Equal ([System.IO.File]::ReadAllText($destination)) 'first release command' 'Installed command bytes'
    }
    default { throw "Unknown scenario: $Scenario" }
}
