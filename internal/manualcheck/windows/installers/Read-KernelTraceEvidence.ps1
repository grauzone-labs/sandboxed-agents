#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$Evidence,[Parameter(Mandatory)][string]$Csv,[Parameter(Mandatory)][uint32[]]$Owners,[Parameter(Mandatory)][string]$Method,[Parameter(Mandatory)][string]$CriterionSha256,[Parameter(Mandatory)][string]$Candidate)
$ErrorActionPreference='Stop'
# Read-VerifiedArtifact resolves one immutable input and checks its recorded hash before parsing it.
function Read-VerifiedArtifact($Entry) {
    if(-not $Entry -or $Entry.sha256 -notmatch '^[a-fA-F0-9]{64}$' -or -not $Entry.path){throw 'Missing artifact/hash'}
    $Path=(Resolve-Path -LiteralPath $Entry.path).ProviderPath
    if((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -ine $Entry.sha256){throw 'Evidence artifact hash changed'}
    return $Path
}
# Read-Utc normalizes procedure timestamps without culture-specific conversion or cross-tool event matching.
function Read-Utc($Value) {
    if($Value -is [datetime]){return [DateTimeOffset]::new($Value.ToUniversalTime())}
    return [DateTimeOffset]::Parse([string]$Value,[Globalization.CultureInfo]::InvariantCulture)
}
$Limit='This evidence does not identify the trace event of a row. It shows that the kernel trace recorded every write of the row''s thread to `$LogFile` with `Ntfs.sys` on its stack. It assumes that, while both tools run, the kernel trace records every write request that Process Monitor records, as a file write of the same thread with the same offset, length, and flags. Neither tool documents this; the counts in this capture are consistent with it.'
$Result=[ordered]@{schema_version=2;complete=$false;failure='';accepted_full_ordinals=@();saved_ntfs_full_ordinals=@();groups=@();assumption=$Limit;raw_and_export_hashes=@();pinned_tools=@()}
try {
    if($CriterionSha256 -notmatch '^[0-9a-f]{64}$' -or $Candidate -notmatch '^[0-9a-f]{40}$'){throw 'Kernel fallback requires the exact criterion revision hash and candidate'}
    $Bundle=Get-Content -Raw -LiteralPath $Evidence|ConvertFrom-Json
    if($Bundle.schema_version -ne 2){throw 'Kernel evidence schema must be2'}
    $Paths=@{}
    foreach($Role in @('pml','procmon_xml','volume_before','volume_after')){
        $Entry=$Bundle.artifacts.$Role;$Paths[$Role]=Read-VerifiedArtifact $Entry
        $Result.raw_and_export_hashes+=@{role=$Role;sha256=$Entry.sha256.ToLowerInvariant()}
    }
    $CsvHash=(Get-FileHash -LiteralPath $Csv).Hash.ToLowerInvariant();$Export=Get-Content -Raw -LiteralPath ($Csv+'.export.json')|ConvertFrom-Json
    if($Export.exportAppliedDisplayFilter -isnot [bool] -or $Export.exportAppliedDisplayFilter -ne $false -or $Export.csvSha256 -ine $CsvHash -or $Export.sourcePmlSha256 -ine $Bundle.artifacts.pml.sha256){throw 'Full export provenance does not bind this PML/CSV'}
    $Result.raw_and_export_hashes+=@{role='csv';sha256=$CsvHash}
    $Before=Get-Content -Raw -LiteralPath $Paths.volume_before|ConvertFrom-Json;$After=Get-Content -Raw -LiteralPath $Paths.volume_after|ConvertFrom-Json
    foreach($V in @($Before,$After)){
        if($V.drive -notmatch '^[A-Za-z]:$' -or $V.nt_device -notmatch '^\\Device\\HarddiskVolume[0-9]+$' -or $V.filesystem -cne 'NTFS' -or $V.drive_type -cne 'Fixed' -or $V.windows_directory -notmatch '^[A-Za-z]:\\[^\\]+$'){throw 'QueryDosDevice/fixed NTFS/SystemRoot evidence missing'}
    }
    foreach($Field in @('drive','nt_device','filesystem','drive_type','windows_directory')){if($Before.$Field -ine $After.$Field){throw 'Volume association changed'}}
    if(-not ('InstallerKernelTrace' -as [type])){Add-Type -Path (Join-Path $PSScriptRoot 'KernelTrace.cs')}
    $Pm=[InstallerKernelTrace]::ReadProcmon($Csv,$Paths.procmon_xml)
    $Result.saved_ntfs_full_ordinals=[InstallerKernelTrace]::SavedNtfs($Pm,$Owners,[string]$Before.drive,[string]$Before.windows_directory)
    $Result.volume_before=$Before;$Result.volume_after=$After
    foreach($Role in @('etl','kernel_xml','kernel_summary','capture','pins')){
        $Entry=$Bundle.artifacts.$Role;$Paths[$Role]=Read-VerifiedArtifact $Entry
        $Result.raw_and_export_hashes+=@{role=$Role;sha256=$Entry.sha256.ToLowerInvariant()}
    }
    if(@($Paths.Values|Sort-Object -Unique).Count -ne $Paths.Count){throw 'Artifact roles must identify distinct files'}
    $Capture=Get-Content -Raw -LiteralPath $Paths.capture|ConvertFrom-Json
    if($Capture.schema_version -ne 2 -or $Capture.method -cne $Method -or $Capture.criterion_sha256 -cne $CriterionSha256 -or $Capture.captured_commit -cne $Candidate){throw 'Method/criterion/candidate capture mismatch'}
    foreach($Flag in @('drop_filtered_events','advanced_output','export_applied_display_filter')){if($Capture.settings.$Flag -isnot [bool] -or $Capture.settings.$Flag -ne $false){throw 'Capture/export settings not proven'}}
    if($Capture.settings.human_confirmed -isnot [bool] -or -not $Capture.settings.human_confirmed -or @('File System','Registry','Process/Thread'|Where-Object{$Capture.settings.activity -cnotcontains $_}).Count){throw 'Human capture readiness/activity confirmation absent'}
    $Start=Read-Utc $Capture.wpr_start_utc;$Stop=Read-Utc $Capture.wpr_stop_utc
    if(-not ($Start -lt (Read-Utc $Capture.procmon.start_utc) -and (Read-Utc $Capture.procmon.start_utc) -lt (Read-Utc $Capture.procmon.end_utc) -and (Read-Utc $Capture.procmon.end_utc) -lt $Stop)){throw 'WPR procedure does not enclose Procmon start/end'}
    $Pins=Get-Content -Raw -LiteralPath $Paths.pins|ConvertFrom-Json
    if($Pins.schema_version -ne 2 -or (Read-Utc $Pins.created_utc) -ge $Start){throw 'Tools/profile were not pinned before capture'}
    $PinPaths=[Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach($Pin in $Pins.files){$P=Read-VerifiedArtifact $Pin;if(-not $PinPaths.Add($P)){throw 'Duplicate tool pin'};$Result.pinned_tools+=@{file=[IO.Path]::GetFileName($P);sha256=$Pin.sha256.ToLowerInvariant()}}
    foreach($NativeName in @('Procmon64.exe','wpr.exe','tracerpt.exe')) {
        if(@($Pins.files|Where-Object{[IO.Path]::GetFileName($_.path) -ieq $NativeName}).Count -ne 1){throw ('Native recording/export tool missing or ambiguous in pre-capture pins: '+$NativeName)}
    }
    foreach($Required in (Get-ChildItem -LiteralPath $PSScriptRoot -File -Recurse|Where-Object{$_.Extension -in @('.ps1','.cs','.py','.wprp')})) {if(-not $PinPaths.Contains($Required.FullName)){throw 'Analysis tool/test/profile missing from pre-capture pins'}}
    if($Capture.profile_sha256 -notmatch '^[a-fA-F0-9]{64}$' -or -not $Capture.profile_path -or @($Pins.files|Where-Object{$_.path -ieq $Capture.profile_path -and $_.sha256 -ieq $Capture.profile_sha256}).Count -ne 1){throw 'Recording profile differs from pre-capture pin'}
    if(-not @($Pins.files|Where-Object{[IO.Path]::GetExtension($_.path) -eq '.wprp'}).Count){throw 'Recording profile not pinned'}
    if((Read-Utc $Before.utc) -ge $Start -or (Read-Utc $After.utc) -le $Stop){throw 'Volume queries do not enclose capture'}
    if($IsWindows -and $Paths.pml -ine $Capture.procmon.backing_file){throw 'Captured PML is not the Procmon command backing file'}
    $Proof=[InstallerKernelTrace]::Read($Pm,$Paths.kernel_xml,$Owners,[uint32]$Capture.procmon.pid,[string]$Capture.procmon.path,[string]$Capture.procmon.backing_file,[string]$Capture.procmon.command_line,[string]$Before.nt_device,[string]$Before.drive,[string]$Before.windows_directory)
    foreach($Property in $Proof.PSObject.Properties){$Result[$Property.Name]=$Property.Value}
    $Result.volume_before=$Before;$Result.volume_after=$After;$Result.complete=$true
} catch {
    $Result.complete=$false;$Result.accepted_full_ordinals=@();$Result.groups=@();$Result.failure=$_.Exception.Message
}
[pscustomobject]$Result
