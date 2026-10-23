#Requires -Version 7.0
<#
Automatic LOCAL triage of a complete Process Monitor CSV. Prefer -ShellPid to
infer descendants from unfiltered Process Create/Start events. -ProcessIds
supports the earlier workflow with an independently reviewed process tree.
Rows are counted automatically; ExpectedRows is an optional legacy cross-check.
Detailed paths/data remain in .private-review.json; share only the safe summary.
NO_FINDINGS is NOT an acceptance pass: capture completeness, environment and
command exits still require verification. No checklist is ever modified.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Csv,
    [ValidateRange(0, 2147483647)][int]$ExpectedRows = 0,
    [int[]]$ProcessIds = @(),
    [int]$ShellPid = 0,
    [Parameter(Mandatory)][string]$InstallRoot,
    [ValidateSet("binary","npm","nuget-root","nuget-default")][string]$Method = "binary",
    [string]$DefaultInstallRoot = "",
    [string]$UserSid = "",
    [ValidateSet('literal','windows-bookkeeping-v1')][string]$BookkeepingCriterion = 'literal',
    [string]$CriterionSha256 = '',
    [string]$OutputDirectory = '',
    [string]$BookkeepingEvidence = '',
    [string]$KernelTraceEvidence = '',
    [string]$Candidate = ''
)

$ErrorActionPreference = 'Stop'
if (($ShellPid -le 0 -and $ProcessIds.Count -eq 0) -or @($ProcessIds | Where-Object { $_ -le 0 }).Count) {
    throw 'Supply every positive PID from the reviewed item process tree.'
}
$Root = $InstallRoot.Replace('/', '\').TrimEnd('\')
if ($Root -notmatch '^[A-Za-z]:\\[^\\]+' -or $Root -match '(^|\\)\.\.?($|\\)') {
    throw 'InstallRoot must be the specific absolute Windows test directory.'
}
$DefaultRoot = $DefaultInstallRoot.Replace('/', '\').TrimEnd('\')
if ($Method -eq 'nuget-default' -and $DefaultRoot -notmatch '^[A-Za-z]:\\[^\\]+') { throw 'Supply absolute DefaultInstallRoot for nuget-default.' }
$CsvPath = (Resolve-Path -LiteralPath $Csv).ProviderPath
$ReportBase=$CsvPath
if($BookkeepingCriterion -ne 'literal') {
    if($CriterionSha256 -notmatch '^[a-fA-F0-9]{64}$' -or -not $OutputDirectory) { throw 'Revised criterion requires its SHA256 and a separate output directory.' }
}
if($OutputDirectory) {
    [void][IO.Directory]::CreateDirectory([IO.Path]::GetFullPath($OutputDirectory))
    $ReportBase=Join-Path ([IO.Path]::GetFullPath($OutputDirectory)) ([IO.Path]::GetFileName($CsvPath))
}
if($BookkeepingCriterion -ne 'literal' -and $ReportBase -eq $CsvPath) { throw 'Revised reports must not replace original reports.' }
foreach($Output in @($ReportBase+'.summary.json',$ReportBase+'.private-review.json')) {
    if(Test-Path -LiteralPath $Output) { throw 'Output exists; no prior report overwritten.' }
}
$Rows = @(Import-Csv -LiteralPath $CsvPath)
if ($Rows.Count -eq 0) { throw "INCOMPLETE: empty export" }
if ($ExpectedRows -gt 0 -and $Rows.Count -ne $ExpectedRows) {
    throw "INCOMPLETE: expected $ExpectedRows exported rows, found $($Rows.Count). Remove temporary Operation/Result/Path/Detail filters and export all displayed events."
}
$Required = @('Process Name', 'PID', 'Operation', 'Path', 'Result', 'Detail')
foreach ($Column in $Required) {
    if ($Rows[0].PSObject.Properties.Name -notcontains $Column) {
        throw "Missing CSV column: $Column. Use Process Monitor CSV export."
    }
}


$AllRows = $Rows
for($FullIndex=0;$FullIndex -lt $AllRows.Count;$FullIndex++){ $AllRows[$FullIndex]|Add-Member -NotePropertyName FullOrdinal -NotePropertyValue ($FullIndex+1) }
$Owned = [Collections.Generic.HashSet[int]]::new()
# Process edges and the full ordinal of each Process Create row serve the scope inference and the
# inherited anonymous-pipe rule, whatever the scope mode.
$Edges = @()
$CreateOrdinal = @{}
foreach ($Event in $AllRows) {
    $EventId = 0
    if (-not [int]::TryParse([string]$Event.PID, [ref]$EventId)) { throw 'INCOMPLETE: invalid PID in full export' }
    if ($Event.Operation -eq 'Process Create' -and $Event.Detail -match '(?:^|,\s*)PID:\s*(\d+)') {
        $Edges += [pscustomobject]@{ Parent=$EventId; Child=[int]$Matches[1] }
        if (-not $CreateOrdinal.ContainsKey([int]$Matches[1])) { $CreateOrdinal[[int]$Matches[1]] = [long]$Event.FullOrdinal }
    } elseif ($Event.Operation -eq 'Process Start' -and $Event.Detail -match '(?:^|,\s*)Parent PID:\s*(\d+)') {
        $Edges += [pscustomobject]@{ Parent=[int]$Matches[1]; Child=$EventId }
    }
}
if ($ShellPid -gt 0) {
    [void]$Owned.Add($ShellPid)
    do {
        $Added = $false
        foreach ($Edge in $Edges) { if ($Owned.Contains($Edge.Parent) -and $Owned.Add($Edge.Child)) { $Added=$true } }
    } while ($Added)
    $ProcessIds = @($Owned | Sort-Object)
} else { foreach ($Value in $ProcessIds) { [void]$Owned.Add($Value) } }
# ParentOf maps an attributed child to its one attributed parent; a child with two parents has none.
$ParentOf = @{}
$AmbiguousParent = [Collections.Generic.HashSet[int]]::new()
foreach ($Edge in $Edges) {
    if (-not ($Owned.Contains($Edge.Parent) -and $Owned.Contains($Edge.Child)) -or $Edge.Parent -eq $Edge.Child) { continue }
    if ($ParentOf.ContainsKey($Edge.Child) -and $ParentOf[$Edge.Child] -ne $Edge.Parent) { [void]$AmbiguousParent.Add($Edge.Child) }
    $ParentOf[$Edge.Child] = $Edge.Parent
}
foreach ($Child in $AmbiguousParent) { $ParentOf.Remove($Child) }
$OwnedEvents = @($AllRows | Where-Object { $Owned.Contains([int]$_.PID) })
foreach ($Value in $ProcessIds) {
    if (-not @($OwnedEvents | Where-Object { [int]$_.PID -eq $Value }).Count) { throw "INCOMPLETE: expected PID $Value absent" }
    if (@($OwnedEvents | Where-Object { [int]$_.PID -eq $Value -and $_.Operation -eq 'Process Start' }).Count -gt 1) { throw "INCOMPLETE: PID $Value reused during capture" }
}
# Test-PodmanProcessMetadata checks executable names and command tokens, never environment blocks.
function Test-PodmanProcessMetadata($Event) {
    $ExecutableName = '^(?i:podman(?:-remote)?)(?:\.(?i:exe|cmd|bat|com))?$'
    $Name = ([string]$Event.'Process Name').Replace('/', '\').Split('\')[-1]
    if ($Name -match $ExecutableName) { return $true }
    if ($Event.Operation -notin @('Process Create','Process Start')) { return $false }
    $ImageName = ([string]$Event.Path).Replace('/', '\').Split('\')[-1]
    if ($ImageName -match $ExecutableName) { return $true }
    # Process Start Detail also contains the inherited environment, including our fake PATH.
    $Metadata = ([string]$Event.Detail -split '(?i)(?:,\s*|\r?\n\s*)Environment:', 2)[0]
    if ($Metadata -match '(?i)(?:^|,\s*)Command line:\s*(.*?)(?:,\s*Current directory:|$)') {
        $CommandLine = $Matches[1]
        # Also detect an absolute Podman invocation wrapped by cmd or another shell.
        return $CommandLine -match '(?i)(?:^|[\\/\s"''])podman(?:-remote)?(?:\.(?:exe|cmd|bat|com))?(?=$|[\s"''])'
    }
    return $false
}

$PodmanPids = [Collections.Generic.HashSet[int]]::new()
foreach ($Event in $OwnedEvents) {
    if (Test-PodmanProcessMetadata $Event) {
        if ($Event.Operation -eq 'Process Create' -and $Event.Detail -match '(?:^|,\s*)PID:\s*(\d+)') {
            [void]$PodmanPids.Add([int]$Matches[1])
        } else { [void]$PodmanPids.Add([int]$Event.PID) }
    }
}
$NonFileRegistry = @('Process Create','Process Start','Process Exit','Thread Create','Thread Exit','Load Image','Process Profiling','Thread Profiling')
$Rows = @($OwnedEvents | Where-Object { $NonFileRegistry -notcontains $_.Operation -and $_.Operation -notmatch '^(TCP|UDP) ' })
$DefaultParent = $DefaultRoot -replace '\\[^\\]+$', ''
$CreatedDefaultParent = $Method -eq 'nuget-default' -and @($Rows | Where-Object {
    $_.Operation -eq 'CreateFile' -and $_.Result -eq 'SUCCESS' -and
    $_.Path -ieq $DefaultParent -and $_.Detail -match '\bOpenResult:\s*Created\b' -and
    $_.Detail -match '\bOptions:.*?\bDirectory\b'
}).Count -gt 0

# Get-LocationClass checks Windows directory boundaries without resolving files.
function Get-LocationClass([string]$EventPath) {
    $Value = $EventPath.Replace('/', '\')
    if ($Value.StartsWith('\\?\', [StringComparison]::Ordinal)) {
        $Value = $Value.Substring(4)
    }
    if ($Value.StartsWith('\??\', [StringComparison]::Ordinal)) {
        $Value = $Value.Substring(4)
    }
    if ($Value -match '(^|\\)\.\.?($|\\)') { return 'UnresolvedPath' }
    if ($Value -match '^(\\Device\\NamedPipe\\|\\\\\.\\pipe\\)') { return 'NamedPipe' }
    if ($Value.Equals($Root, [StringComparison]::OrdinalIgnoreCase) -or
        $Value.StartsWith($Root + '\', [StringComparison]::OrdinalIgnoreCase)) {
        return 'InsideInstallRoot'
    }
    if ($Method -eq 'nuget-default') {
        if ($Value -ieq 'HKCU\Environment\Path' -or $Value -ieq 'HKEY_CURRENT_USER\Environment\Path' -or ($UserSid -match '^S-1-[0-9-]+$' -and $Value -ieq ('HKU\' + $UserSid + '\Environment\Path'))) { return 'AllowedUserPath' }
        if ($Value.Equals($DefaultRoot, [StringComparison]::OrdinalIgnoreCase) -or $Value.StartsWith($DefaultRoot + '\', [StringComparison]::OrdinalIgnoreCase)) { return 'InsideDefaultRoot' }
        if ($CreatedDefaultParent -and $Value.Equals($DefaultParent, [StringComparison]::OrdinalIgnoreCase)) { return 'InsideDefaultParent' }
    }
    if ($Value -match '^(HKLM|HKCU|HKU|HKCR|HKCC|HKEY_LOCAL_MACHINE|HKEY_CURRENT_USER|HKEY_USERS|HKEY_CLASSES_ROOT|HKEY_CURRENT_CONFIG)$') { return 'OutsideInstallRoot' }
    if ($Value -match '^([A-Za-z]:\\|\\\\|HK[A-Z_]+\\|HKEY_[A-Z_]+\\)') {
        return 'OutsideInstallRoot'
    }
    return 'UnresolvedPath'
}

$ReadOperations = @(
    'ReadFile', 'CloseFile', 'IRP_MJ_CLOSE', 'SetPositionInformationFile', 'LockFile',
    'UnlockFileSingle', 'FlushBuffersFile', 'RegOpenKey', 'RegQueryKey',
    'RegQueryValue', 'RegEnumKey', 'RegEnumValue', 'RegCloseKey',
    'RegQueryKeySecurity', 'RegFlushKey'
)
$SingleTargetChanges = @(
    'WriteFile', 'SetEndOfFileInformationFile', 'SetAllocationInformationFile',
    'SetValidDataLengthInformationFile', 'SetDispositionInformationFile',
    'SetDispositionInformationEx', 'SetBasicInformationFile', 'SetEAFile',
    'SetSecurityFile', 'SetFileObjectIdInformation', 'RegSetValue',
    'RegSetInfoKey', 'RegDeleteKey', 'RegDeleteValue', 'RegSetKeySecurity'
)
$ComplexChanges = @(
    'SetRenameInformationFile', 'SetRenameInformationEx',
    'SetLinkInformationFile', 'SetShortNameInformation', 'RegRenameKey',
    'RegLoadKey', 'RegUnLoadKey', 'RegRestoreKey', 'RegReplaceKey', 'RegSaveKey'
)
$Conditional = @('CreateFile', 'RegCreateKey', 'CreateFileMapping', 'FileSystemControl', 'DeviceIoControl')
$PlainFailures = @('ACCESS DENIED', 'NAME NOT FOUND', 'PATH NOT FOUND', 'NAME COLLISION', 'SHARING VIOLATION')
$StackEvents=@()
$CachePaths=[Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
$EvidenceSha256=''
$WindowsDirectory=''
$Metadata=$null
if($BookkeepingEvidence) {
    if($BookkeepingCriterion -eq 'literal') { throw 'Bookkeeping evidence requires the revised criterion.' }
    $EvidenceSha256=(Get-FileHash -LiteralPath $BookkeepingEvidence -Algorithm SHA256).Hash.ToLowerInvariant()
    $Metadata=Get-Content -Raw -LiteralPath $BookkeepingEvidence|ConvertFrom-Json
    $ManifestPath=$CsvPath+'.export.json'
    if(-not (Test-Path -LiteralPath $ManifestPath)) { throw 'Evidence requires verified full-export provenance.' }
    $ExportProof=Get-Content -Raw -LiteralPath $ManifestPath|ConvertFrom-Json
    if($Metadata.schema_version -ne 1 -or $Metadata.csv_sha256 -ine (Get-FileHash -LiteralPath $CsvPath -Algorithm SHA256).Hash -or
       $ExportProof.csvSha256 -ine $Metadata.csv_sha256 -or $ExportProof.sourcePmlSha256 -ine $Metadata.source_pml_sha256 -or
       $ExportProof.exportAppliedDisplayFilter -ne $false -or $Metadata.source_xml_sha256 -notmatch '^[a-fA-F0-9]{64}$') { throw 'Evidence belongs to a different or unverified source capture.' }
    foreach($Artifact in @(@{file=$Metadata.stack_report_file;hash=$Metadata.stack_report_sha256},@{file=$Metadata.ea_query_file;hash=$Metadata.ea_query_sha256})) {
        if($Artifact.hash -notmatch '^[a-fA-F0-9]{64}$' -or (Get-FileHash -LiteralPath $Artifact.file -Algorithm SHA256).Hash -ine $Artifact.hash) { throw 'Bookkeeping evidence artifact changed.' }
    }
    $StackReport=Get-Content -Raw -LiteralPath $Metadata.stack_report_file|ConvertFrom-Json
    if($StackReport.exported_events -ne $AllRows.Count) { throw 'Stack report event count differs from the full capture.' }
    $StackEvents=@($StackReport.events)
    $WindowsDirectory=([string]$Metadata.windows_directory).TrimEnd('\')
    if($WindowsDirectory -notmatch '^[A-Za-z]:\\[^\\]+$') { throw 'Evidence must name the captured host Windows directory.' }
}
# Test-SystemStack requires every matching event occurrence to contain the actual system module.
function Test-SystemStack([int]$EventProcess,[string]$Operation,[string]$EventPath,[string]$EventResult,[string]$EventDetail,[string]$Module,[int]$ExpectedCount) {
    $Matching=@($StackEvents|Where-Object { $_.pid -eq $EventProcess -and $_.operation -eq $Operation -and $_.path -ieq $EventPath -and $_.result -eq $EventResult -and $_.detail -ceq $EventDetail })
    if($Matching.Count -ne $ExpectedCount -or $ExpectedCount -eq 0 -or -not $WindowsDirectory) { return $false }
    $ModulePath=$WindowsDirectory+'\System32\'+$Module
    foreach($Event in $Matching) {
        if(@($Event.frames|Where-Object { $_.path -ieq $ModulePath }).Count -eq 0) { return $false }
    }
    return $true
}
if($Metadata) {
    $EaRoot=([string]$Metadata.ea_query_root).TrimEnd('\')
    if($EaRoot -notmatch '^[A-Za-z]:\\' -or $EaRoot -match '(^|\\)\.\.?($|\\)') { throw 'Invalid EA query root.' }
    $Queries=@(Get-Content -Raw -LiteralPath $Metadata.ea_query_file|ConvertFrom-Json)
    foreach($Query in $Queries) {
        $CapturedTime=[DateTimeOffset]::MinValue
        # ConvertFrom-Json materializes ISO timestamps as DateTime in newer PowerShell.
        # Keep their round-trip form rather than converting through the host culture.
        $QueryTime=if($Query.capturedUtc -is [DateTime]) { $Query.capturedUtc.ToString('o',[Globalization.CultureInfo]::InvariantCulture) } else { [string]$Query.capturedUtc }
        if($Query.queryExit -ne 0 -or @($Query.eaNames).Count -ne 1 -or $Query.eaNames[0] -ine '$KERNEL.PURGE.ESBCACHE' -or
           $Query.file -notmatch '^[^\\/:]+\.dll$' -or -not [DateTimeOffset]::TryParse($QueryTime,[Globalization.CultureInfo]::InvariantCulture,[Globalization.DateTimeStyles]::None,[ref]$CapturedTime)) { continue }
        $Target=$EaRoot+'\'+$Query.file
        $EaRows=@($Rows|Where-Object { $_.Operation -eq 'SetEAFile' -and $_.Path -ieq $Target })
        if($EaRows.Count -eq 0) { continue }
        $AllQualified=$true
        foreach($Group in ($EaRows|Group-Object PID,Operation,Path,Result,Detail)) {
            $First=$Group.Group[0]
            if($First.Result -ne 'SUCCESS' -or -not (Test-SystemStack ([int]$First.PID) 'SetEAFile' $Target 'SUCCESS' ([string]$First.Detail) 'CI.dll' $Group.Count)) { $AllQualified=$false }
        }
        if($AllQualified) { [void]$CachePaths.Add($Target) }
    }
}

$KernelProof=$null
if($KernelTraceEvidence) {
    if($BookkeepingCriterion -eq 'literal'){throw 'Kernel evidence requires the revised criterion.'}
    $KernelProof=& (Join-Path $PSScriptRoot 'Read-KernelTraceEvidence.ps1') -Evidence $KernelTraceEvidence -Csv $CsvPath -Owners ([uint32[]]$ProcessIds) -Method $Method -CriterionSha256 $CriterionSha256 -Candidate $Candidate
}

$PipeQueryProof=$null
if($KernelTraceEvidence){
    $PipeQueryProof=& (Join-Path $PSScriptRoot 'Read-PmlPipeQueries.ps1') -Evidence $KernelTraceEvidence -Csv $CsvPath -Rows $AllRows -Owners ([uint32[]]$ProcessIds)
}
$AcceptedPipeQueries=[Collections.Generic.HashSet[long]]::new()
if($PipeQueryProof -and $PipeQueryProof.complete){ foreach($Ordinal in @($PipeQueryProof.accepted_full_ordinals)){ [void]$AcceptedPipeQueries.Add([long]$Ordinal) } }

# BarePipeCreates lists each successful CreatePipe on the bare \Device\NamedPipe, an anonymous pipe,
# by attributed process and full ordinal, in log order.
$BarePipeCreates=@()
# Test-InheritedBarePipe tells whether a row on the bare \Device\NamedPipe can use an anonymous pipe:
# the same process created one earlier, or an attributed ancestor created one before it created the
# next process on the chain down to the row's process, so that the handle could be inherited. It
# claims nothing about which pipe the handle is; the log does not name anonymous pipes.
function Test-InheritedBarePipe([int]$RowPid,[long]$RowOrdinal) {
    foreach($Create in $BarePipeCreates) {
        if($Create.Ordinal -ge $RowOrdinal){continue}
        if($Create.Pid -eq $RowPid){return $true}
        $Child=$RowPid;$Hops=0
        while($ParentOf.ContainsKey($Child) -and $Hops -lt 64) {
            $Parent=$ParentOf[$Child]
            if($Parent -eq $Create.Pid) {
                if($CreateOrdinal.ContainsKey($Child) -and $CreateOrdinal[$Child] -gt $Create.Ordinal -and $CreateOrdinal[$Child] -lt $RowOrdinal){return $true}
                break
            }
            $Child=$Parent;$Hops++
        }
    }
    return $false
}
$Seen = [Collections.Generic.HashSet[int]]::new()
$Classified = for ($Index = 0; $Index -lt $Rows.Count; $Index++) {
    $Row = $Rows[$Index]
    $EventPid = 0
    if (-not [int]::TryParse($Row.PID, [ref]$EventPid) -or $ProcessIds -notcontains $EventPid) {
        throw "Wrong process scope at CSV row $($Index + 1)."
    }
    [void]$Seen.Add($EventPid)
    foreach ($Column in @('Process Name', 'Operation', 'Path', 'Result')) {
        if ([string]::IsNullOrWhiteSpace([string]$Row.$Column)) {
            throw "Missing $Column value at CSV row $($Index + 1)."
        }
    }

    $Op = ([string]$Row.Operation).Trim()
    $Result = ([string]$Row.Result).Trim()
    $Detail = [string]$Row.Detail
    $Location = Get-LocationClass ([string]$Row.Path)
    $BareCreate = $Op -eq 'CreatePipe' -and $Result -eq 'SUCCESS' -and $Row.Path -ieq '\Device\NamedPipe'
    if($BookkeepingCriterion -ne 'literal' -and $Row.Path -ieq '\Device\NamedPipe' -and ($BareCreate -or (Test-InheritedBarePipe $EventPid ([long]$Row.FullOrdinal)))) { $Location='NamedPipe' }
    if($BareCreate) { $BarePipeCreates+=@{Pid=$EventPid;Ordinal=[long]$Row.FullOrdinal} }
    $Category = 'NeedsReview'
    $Reason = 'Unresolved operation, result, or details'
    $IsChange = $false
    $PipeQuery = $false
    $BookkeepingKind=''
    $KnownMutation = ($SingleTargetChanges -contains $Op) -or ($ComplexChanges -contains $Op) -or ($Conditional -contains $Op)

    # These exact FSCTLs query/read the source; never infer other control codes are reads.
    $IsReadControl = $Op -eq 'FileSystemControl' -and $Detail -match '\bControl:\s*(FSCTL_QUERY_USN_JOURNAL|FSCTL_READ_FILE_USN_DATA|FSCTL_OFFLOAD_READ)\b'
    if($BookkeepingCriterion -ne 'literal' -and $Op -eq 'FileSystemControl' -and $Result -eq 'INVALID DEVICE REQUEST' -and
       $Detail -ceq 'Control: 0x90390 (Device:0x9 Function:228 Method: 0)') {
        $Category='FailedNoChange';$Reason='Exact 0x90390 = FSCTL_QUERY_VOLUME_CONTAINER_STATE (Microsoft binding590736); target rejected this request'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -eq 'FileSystemControl' -and $Detail -match '^Control:\s*FSCTL_QUERY_FILE_REGIONS(?:,|$)') {
        $Category='ReadQueryClose';$Reason='Exact MS-FSCC file-region query; no generic query-name/control wildcard'
    } elseif ($BookkeepingCriterion -ne 'literal' -and $Op -eq 'RegSetInfoKey'  -and $Result -eq 'SUCCESS' -and
        $Detail -match '^KeySetInformationClass:\s*KeySetHandleTagsInformation,\s*Length:\s*0$' -and
        [string]$Row.Path -match '^(HKLM|HKCU|HKU|HKCR|HKCC|HKEY_LOCAL_MACHINE|HKEY_CURRENT_USER|HKEY_USERS|HKEY_CLASSES_ROOT|HKEY_CURRENT_CONFIG)(\\|$)') {
        $Category='HandleBookkeeping';$BookkeepingKind='registry-handle-tags'
        $Reason='Exact per-handle registry tags; CM_KEY_BODY HandleTags, not persistent key/value state (Project Zero original research)'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -in @('FASTIO_ACQUIRE_FOR_CC_FLUSH','FASTIO_RELEASE_FOR_CC_FLUSH','FASTIO_RELEASE_FOR_SECTION_SYNCHRONIZATION','FASTIO_MDL_READ_COMPLETE') -and $Result -eq 'SUCCESS') {
        $Category='ReadQueryClose';$Reason='Exact filesystem resource synchronization or MDL read completion; not the later I/O itself'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -eq 'WriteFile' -and $Result -eq 'FAST IO DISALLOWED') {
        $Category='FailedNoChange';$Reason='Fast path declined before reaching filesystem; any IRP retry is classified separately'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -eq 'CreateFileMapping' -and $Result -in @('SUCCESS','FILE LOCKED WITH ONLY READERS','FILE LOCKED WITH WRITERS')) {
        if($Detail -match '^SyncType:\s*SyncTypeOther(?:,\s*PageProtection:\s*0)?$' -or
           $Detail -match '^SyncType:\s*SyncTypeCreateSection,\s*PageProtection:\s*(PAGE_READONLY|PAGE_EXECUTE|PAGE_EXECUTE_READ)$') {
            $Category='ReadQueryClose';$Reason='Exact synchronization-only or read/execute/private-copy section protection; no shared file write'
        } elseif($Detail -match '^SyncType:\s*SyncTypeCreateSection,\s*PageProtection:\s*(PAGE_READWRITE|PAGE_EXECUTE_READWRITE)$' -and $Location -in @('InsideInstallRoot','InsideDefaultRoot')) {
            $Category='AllowedWritableMapping';$Reason='Potential mapped writes target an allowed file; not counted as actual WriteFile rows'
        } else { $Reason='Writable or unresolved section outside allowed files; absence of attributed WriteFile does not prove unchanged data' }
    } elseif($BookkeepingCriterion -ne 'literal' -and (
        ($Op -eq 'CreateFile' -and $Result -in @('NAME INVALID','DELETE PENDING')) -or
        ($Op -in @('SetDispositionInformationEx','SetDispositionInformationFile') -and $Result -eq 'NOT EMPTY'))) {
        $Category='FailedNoChange';$Reason='Specific rejected open/delete/unsupported request; successful or different requests remain reviewed'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Location -eq 'NamedPipe' -and $Row.Path -ine '\Device\NamedPipe' -and (
        ($Op -eq 'CreatePipe' -and $Result -in @('SUCCESS','INSTANCE NOT AVAILABLE')) -or
        ($Op -eq 'SetPipeInformation' -and $Result -eq 'SUCCESS') -or
        ($Op -eq 'FileSystemControl' -and $Detail -match '^Control:\s*FSCTL_PIPE_LISTEN(?:,|$)' -and $Result -in @('SUCCESS','PIPE CONNECTED')))) {
        $Category='NamedPipeCommunication';$Reason='Known pipe creation/mode/listen operation on a named pipe instance; unknown pipe operations remain blocked'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Location -eq 'NamedPipe' -and $Row.Path -ieq '\Device\NamedPipe' -and (
        $BareCreate -or ($Op -eq 'SetPipeInformation' -and $Result -eq 'SUCCESS'))) {
        $Category='NamedPipeCommunication';$Reason='Anonymous pipe created by this process, or whose creation by an attributed ancestor preceded the creation of the next process on the chain; other bare-device operations stay unknown'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -eq '<Unknown>' -and $Location -eq 'NamedPipe' -and $Row.Path -ine '\Device\NamedPipe' -and $AcceptedPipeQueries.Contains([long]$Row.FullOrdinal)) {
        $Category='ReadQueryClose';$PipeQuery=$true;$Reason='Pipe information query (FilePipeInformation or FilePipeLocalInformation) proven at this ordinal by the raw log, the CSV row, and the XML event'
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -eq 'SetEAFile' -and $Result -eq 'SUCCESS' -and $Location -eq 'OutsideInstallRoot') {
        $BookkeepingKind='ci-signature-cache-ea'
        if($CachePaths.Contains([string]$Row.Path)) { $Category='KernelBookkeeping';$Reason='All EA rows have system CI.dll stacks and exact post-test kernel signature-cache EA query; historical payload not recorded' }
        else { $Category='NeedsReview';$Reason='Potential kernel signature-cache metadata lacks complete matching stack/EA-query evidence' }
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -eq 'FileSystemControl' -and $Detail -ceq 'Control: FSCTL_WRITE_USN_CLOSE_RECORD' -and $Result -eq 'SUCCESS') {
        if($Location -in @('InsideInstallRoot','InsideDefaultRoot')) { $IsChange=$true }
        else {
            $BookkeepingKind='ci-signature-cache-usn-close'
            $Count=@($Rows|Where-Object { $_.PID -eq $EventPid -and $_.Operation -eq $Op -and $_.Path -ieq $Row.Path -and $_.Result -eq $Result -and $_.Detail -ceq $Detail }).Count
            if($CachePaths.Contains([string]$Row.Path) -and (Test-SystemStack $EventPid $Op ([string]$Row.Path) $Result $Detail 'CI.dll' $Count)) { $Category='KernelBookkeeping';$Reason='Documented signature-cache USN close, with matching system CI.dll stack and kernel-EA query' }
            else { $Category='NeedsReview';$Reason='Outside USN close lacks complete signature-cache proof' }
        }
    } elseif($BookkeepingCriterion -ne 'literal' -and $Op -eq 'WriteFile' -and $Result -eq 'SUCCESS' -and [string]$Row.Path -match '^[A-Za-z]:\\\$LogFile$' -and $Detail -match '\bNon-cached\b' -and $Detail -match '\bPaging I/O\b') {
        $BookkeepingKind='ntfs-logfile-write'
        $Count=@($Rows|Where-Object { $_.PID -eq $EventPid -and $_.Operation -eq $Op -and $_.Path -ieq $Row.Path -and $_.Result -eq $Result -and $_.Detail -ceq $Detail }).Count
        $DirectOpen=@($Rows|Where-Object { $_.Operation -eq 'CreateFile' -and $_.Path -ieq $Row.Path }).Count -gt 0
        $Volume=([string]$Row.Path).Substring(0,3)
        if($DirectOpen) { $Category='OutsideChangeCandidate';$Reason='Attributed process opened NTFS journal directly; no bookkeeping exemption' }
        elseif($KernelProof -and (($KernelProof.complete -and $KernelProof.accepted_full_ordinals -contains $Row.FullOrdinal) -or $KernelProof.saved_ntfs_full_ordinals -contains $Row.FullOrdinal)) { $Category='KernelBookkeeping';$Reason='Full-ordinal saved stack or approved whole-thread kernel trace proof; see kernel_trace evidence and assumption' }
        elseif(-not $KernelTraceEvidence -and $Metadata -and @($Metadata.ntfs_volume_roots) -icontains $Volume -and (Test-SystemStack $EventPid $Op ([string]$Row.Path) $Result $Detail 'drivers\Ntfs.sys' $Count)) { $Category='KernelBookkeeping';$Reason='NTFS journal write with system Ntfs.sys stack, verified NTFS volume and no attributed direct open' }
        else { $Category='NeedsReview';$Reason='NTFS journal candidate lacks system stack/volume evidence; timing and path alone do not prove cause' }
    } elseif ($ReadOperations -contains $Op -or $Op -match '^Query[A-Za-z0-9]+$' -or $IsReadControl) {
        $Category = 'ReadQueryClose'
        $Reason = 'Read/query/close class from the checklist'
    } elseif ($KnownMutation -and $PlainFailures -contains $Result) {
        $Category = 'FailedNoChange'
        $Reason = 'Explicit plain failure'
    } elseif ($Op -in @('CreateFile', 'RegCreateKey') -and $Result -in @('REPARSE', 'REPARSE GLOBAL')) {
        $Category = 'Redirect'
        $Reason = 'Redirect itself; follow-up target events remain in the complete export'
    } elseif ($Op -eq 'CreateFile' -and $Result -eq 'SUCCESS') {
        if ($Detail -match '\bOpenResult:\s*(Created|Overwritten|Superseded)\b') {
            $IsChange = $true
        } elseif ($Detail -match '\bOpenResult:\s*Opened\b' -and
                  $Detail -notmatch 'Delete[\s_-]*On[\s_-]*Close|FILE_DELETE_ON_CLOSE') {
            $Category = 'ExistingOpen'
            $Reason = 'Existing-file open itself; later changes are classified separately'
        }
    } elseif ($Op -eq 'RegCreateKey' -and $Result -eq 'SUCCESS') {
        if ($Detail -match '\bREG_CREATED_NEW_KEY\b') {
            $IsChange = $true
        } elseif ($Detail -match '\bREG_OPENED_EXISTING_KEY\b') {
            $Category = 'ExistingOpen'
            $Reason = 'Existing registry key'
        }
    } elseif ($SingleTargetChanges -contains $Op -and $Result -eq 'SUCCESS') {
        $IsChange = $true
    } elseif ($Op -in @('FileSystemControl', 'DeviceIoControl')) {
        $Reason = 'Resolve control-code semantics'
        if ($Detail -match '\b(FSCTL_[A-Z0-9_]+|IOCTL_[A-Z0-9_]+)\b') {
            $Reason += ': ' + $Matches[1]
        }
    } elseif ($Op -eq 'CreateFileMapping') {
        $Reason = 'Resolve mapping purpose and surrounding events'
    } elseif ($ComplexChanges -contains $Op) {
        $Reason = 'Resolve all source/destination paths and effects'
    }

    if ($Op -in @('SetRenameInformationFile','SetRenameInformationEx','SetLinkInformationFile') -and $Result -eq 'SUCCESS') {
        if ($Detail -match '(?:^|,\s*)FileName:\s*(.+)$') {
            $Destination = Get-LocationClass $Matches[1]
            $Allowed = @('InsideInstallRoot','InsideDefaultRoot','InsideDefaultParent')
            if ($Location -in $Allowed -and $Destination -in $Allowed) {
                $Category = 'ChangeInsideRoot'; $Reason = 'Source and destination both inside allowed directories'
            } elseif ($Location -eq 'OutsideInstallRoot' -or $Destination -eq 'OutsideInstallRoot') {
                $Category = 'OutsideChangeCandidate'; $Reason = 'Rename/link source or destination is outside allowed directories'
            } else { $Reason = 'Unresolved rename/link path' }
        } else { $Reason = 'Rename/link destination absent or unsupported detail format' }
    }
    if ($IsChange) {
        switch ($Location) {
            'InsideInstallRoot' { $Category = 'ChangeInsideRoot'; $Reason = 'Successful single-target change inside test directory' }
            'InsideDefaultRoot' { $Category='ChangeAllowedDefault'; $Reason='Default command directory in nuget-default only' }
            'InsideDefaultParent' { $Category='ChangeAllowedDefault'; $Reason='Programs parent created by this nuget-default method, explicitly permitted by checklist' }
            'AllowedUserPath' {
                if ($Op -in @('RegSetValue','RegDeleteValue')) { $Category='ChangeAllowedUserPath'; $Reason='Only the user Path registry value in nuget-default' }
                else { $Category='OutsideChangeCandidate'; $Reason='Registry key/metadata operation is not a write of the allowed Path VALUE' }
            }
             'NamedPipe' {
                if($BookkeepingCriterion -eq 'literal' -or $Op -eq 'WriteFile' -or ($Op -eq 'CreateFile' -and $Row.Path -ine '\Device\NamedPipe')) { $Category='NamedPipeCommunication';$Reason='Known named-pipe data/open operation, not a persistent-file change' }
                else { $Category='NeedsReview';$Reason='Operation is not proved pipe communication; namespace alone is no exemption' }
            }
            'OutsideInstallRoot' { $Category = 'OutsideChangeCandidate'; $Reason = 'Successful change outside test directory; inspect locally' }
            default { $Category = 'NeedsReview'; $Reason = 'Cannot resolve changed path' }
        }
    }
    [pscustomobject]@{
        Row = $Index + 1; FullRow=$Row.FullOrdinal; TID=[string]$Row.TID; PID = $EventPid; ProcessName=[string]$Row.'Process Name'; Operation = $Op; Result = $Result; BookkeepingKind=$BookkeepingKind; PipeQuery=$PipeQuery
        Location = $Location; Category = $Category; Reason = $Reason
        Path = [string]$Row.Path; Detail = $Detail
    }
}


# Reject a bookkeeping family if a separate actual change on that exact path remains.
foreach($Event in @($Classified|Where-Object { $_.Category -in @('HandleBookkeeping','KernelBookkeeping') })) {
    if(@($Classified|Where-Object { $_.Path -ieq $Event.Path -and $_.Category -in @('OutsideChangeCandidate','ChangeInsideRoot','ChangeAllowedDefault','ChangeAllowedUserPath') }).Count) {
        $Event.Category='NeedsReview';$Event.Reason='Another non-bookkeeping change exists on this path; no metadata exemption applied'
    }
}
$Categories = @($Classified | Group-Object Category | Sort-Object Name | ForEach-Object {
    [pscustomobject]@{ Category = $_.Name; Rows = $_.Count }
})
$Groups = @()
$GroupNumber = 0
foreach ($Group in ($Classified |
    Where-Object { $_.Category -in @('NeedsReview', 'OutsideChangeCandidate') } |
    Group-Object Category, Operation, Result, Location, Reason, Detail)) {
    $GroupNumber++
    $First = $Group.Group[0]
    $Groups += [pscustomobject]@{
        Id = ('G{0:D3}' -f $GroupNumber); Count = $Group.Count
        Operation = $First.Operation; Result = $First.Result
        Location = $First.Location; Reason = $First.Reason
        Events = @($Group.Group)
    }
}
$ReportPath = $ReportBase + '.private-review.json'
[ordered]@{
    purpose = 'Local triage only; not a pass verdict'
    exported_rows = $AllRows.Count; filesystem_registry_rows=$Rows.Count; expected_rows = $ExpectedRows
    process_ids = $ProcessIds; categories = $Categories
    review_groups = $Groups; classified_events = @($Classified)
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ReportPath -Encoding utf8

"CSV rows (automatic): $($AllRows.Count); attributed file/registry rows: $($Rows.Count)"
$Categories | Format-Table -AutoSize | Out-String | Write-Output
"Review groups: $($Groups.Count)"
$Groups | Select-Object Id, Count, Operation, Result, Location, Reason |
    Format-Table -Wrap -AutoSize | Out-String -Width 180 | Write-Output
"Private details report: $ReportPath"
'VERDICT: pending human review; no acceptance record changed'

$UnknownRows = @($Classified | Where-Object Category -eq NeedsReview).Count
$OutsideRows = @($Classified | Where-Object Category -eq OutsideChangeCandidate).Count
$ProcessMetadataRows = @($OwnedEvents | Where-Object { $_.Operation -in @('Process Create','Process Start','Process Exit') }).Count
$VerifiedFullExport=$false
$ExportManifestPath=$CsvPath+'.export.json'
if(Test-Path -LiteralPath $ExportManifestPath){
    $Export=Get-Content -LiteralPath $ExportManifestPath -Raw | ConvertFrom-Json
    if($Export.exportAppliedDisplayFilter -is [bool] -and $Export.exportAppliedDisplayFilter -eq $false -and $Export.csvSha256 -ieq (Get-FileHash -LiteralPath $CsvPath -Algorithm SHA256).Hash -and $Export.sourcePmlSha256 -match '^[a-fA-F0-9]{64}$'){$VerifiedFullExport=$true}
    else {throw 'INCOMPLETE: full-export manifest does not match this CSV.'}
}
$TraceVerdict = if ($OutsideRows -gt 0 -or $PodmanPids.Count -gt 0) { 'FAIL' } elseif ($UnknownRows -gt 0 -or ($ShellPid -gt 0 -and $ProcessMetadataRows -eq 0 -and -not $VerifiedFullExport)) { 'INCOMPLETE' } else { 'NO_FINDINGS' }
$Summary = [ordered]@{
    method = $Method
    criterion_policy=$BookkeepingCriterion
    criterion_sha256=$CriterionSha256
    bookkeeping_rows=@($Classified|Where-Object { $_.Category -in @('HandleBookkeeping','KernelBookkeeping') }).Count
    kernel_trace=$KernelProof
    pipe_query_proof=$PipeQueryProof
    pipe_query_rows=@($Classified|Where-Object PipeQuery).Count
    named_pipe_rows=@($Classified|Where-Object Category -eq NamedPipeCommunication).Count
    kernel_trace_evidence_sha256=$(if($KernelTraceEvidence){(Get-FileHash -LiteralPath $KernelTraceEvidence).Hash.ToLowerInvariant()}else{''})
    stable_row_number_basis='FullRow is one-based full CSV/XML ordinal; Row retains the v1 attributed file/registry index'
    bookkeeping_evidence_sha256=$EvidenceSha256
    row_number_basis='one-based attributed filesystem/registry sequence in original export order'
    bookkeeping_groups=@($Classified|Where-Object { $_.Category -in @('HandleBookkeeping','KernelBookkeeping') }|Group-Object BookkeepingKind|ForEach-Object { [ordered]@{kind=$_.Name;rows=$_.Count} })
    csv_sha256=(Get-FileHash -LiteralPath $CsvPath -Algorithm SHA256).Hash.ToLowerInvariant()
    full_export_manifest_verified=$VerifiedFullExport
    scope_basis=$(if($ShellPid -gt 0){'inferred from process events; requires complete unfiltered export'}else{'supplied process IDs; independently verify complete tree'})
    attributed_process_metadata_rows=$ProcessMetadataRows
    podman_processes = $PodmanPids.Count
    unknown_rows = $UnknownRows
    trace_completeness = 'CSV alone cannot prove capture completeness; verify full unfiltered export and capture controls'
    exported_rows = $AllRows.Count
    process_ids = @($ProcessIds | Sort-Object)
    filesystem_registry_rows = $Rows.Count
    allowed_changes = @($Classified | Where-Object { $_.Category -in @('ChangeInsideRoot','ChangeAllowedDefault','ChangeAllowedUserPath') }).Count
    outside_changes = @($Classified | Where-Object Category -eq OutsideChangeCandidate).Count
    operations = @($Classified | Group-Object Operation | Sort-Object Name | ForEach-Object { [ordered]@{ operation=$_.Name; rows=$_.Count } })
    results = @($Classified | Group-Object Result | Sort-Object Name | ForEach-Object { [ordered]@{ result=$_.Name; rows=$_.Count } })
    categories = $Categories
    review_groups = $Groups.Count
    acceptance_record_changed = $false
    verdict = $TraceVerdict
}
$Summary | ConvertTo-Json -Depth 18 | Set-Content -LiteralPath ($ReportBase + '.summary.json') -Encoding utf8

"TRACE_CLASSIFICATION: $TraceVerdict; this is not an acceptance pass"
