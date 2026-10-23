#Requires -Version 7.0
<#
Read-only exports and review of one stopped capture run; no capture or installer starts. It runs
only from the trusted snapshot the observer took before the workload, and every helper,
configuration value, executable path, and bound run record comes from that snapshot, never from the
run directory. Every file it writes goes into the run's protected output directory, and its native
tools start there with the snapshot as their working directory. It writes nothing into the run
directory, and it reads the item shell's saved files there only through Read-BoundInput, then
copies them into the output directory before the review. Each export is written once. The
unfiltered Procmon CSV and stack XML, the raw-time and summary kernel exports, the saved-stack and
extended-attribute evidence, the trace review, and the environment review end in result.json, which
`manual-check installers summarize` reads.
#>
[CmdletBinding()]
param([Parameter(Mandatory)][string]$RunDirectory)
. (Join-Path $PSScriptRoot 'Package.ps1')
Assert-Role $true
# System32 is the working directory until the snapshot is verified, then the snapshot itself.
Set-ProtectedLocation (Get-SystemDirectory)
# Without a trusted snapshot there is no protected place for a failure record; the error is shown only.
$Trusted=Read-TrustedSnapshot $PSScriptRoot
Set-ProtectedLocation $Trusted.Root
$Out=$Trusted.OutputDirectory
if((Get-FinalDirectory $RunDirectory) -ine $Trusted.Bound.run){throw 'The run directory is not the one the trusted snapshot bound'}
# Record-ExportFailure leaves a durable failure beside the raw files in the output directory for any
# error, including the checks before the exports; the raw files are never touched.
function Record-ExportFailure([string]$Message) {
 $Marker=Join-Path $Out 'export.failed.json'
 if(-not (Test-Path -LiteralPath $Marker)){Write-NewJson $Marker @{utc=[DateTime]::UtcNow.ToString('o');failure=$Message;raw_logs_preserved=$true;acceptance_changed=$false}}
}
$NotSaved=$false
try {
$Prepared=$Trusted.Prepared;$Item=$Trusted.Item
$Capture=Get-Content -Raw -LiteralPath (Join-Path $Out 'capture.json')|ConvertFrom-Json
$Stopped=Get-Content -Raw -LiteralPath (Join-Path $Out 'capture.stopped.json')|ConvertFrom-Json
$Inputs=Join-Path $Out 'inputs'
if(Test-Path -LiteralPath $Inputs){throw 'Export already attempted; no duplicate or overwrite'}
# Every saved file is read and checked before the one-shot inputs directory exists, so an export run
# before the item shell saved leaves nothing behind and can simply be run again.
$Saved=[ordered]@{}
foreach($Name in @('item.saved.json','method.steps.private.json','method.environment.private.json')){
 $Saved[$Name]=Read-BoundInput $Trusted.Bound.run $Name
 if($null -eq $Saved[$Name]){$NotSaved=$true;throw "The item shell has not saved $Name yet; save in the item shell, then run the export again"}
}
$Calls=Read-BoundInput $Trusted.Bound.fake_podman 'calls'
$FakeBytes=if($null -eq $Calls){0}else{$Calls.Length}
$Record=[Text.Encoding]::UTF8.GetString($Saved['item.saved.json'])|ConvertFrom-Json
[void](New-Item -ItemType Directory -Path $Inputs)
foreach($Name in $Saved.Keys){Write-NewBytes (Join-Path $Inputs $Name) $Saved[$Name]}
if($null -ne $Calls){Write-NewBytes (Join-Path $Inputs 'fake-podman-calls.private.txt') $Calls}
$Saved=$Record
if($Stopped.nonce -cne $Item.nonce -or $Stopped.procmon_stopped -ne $true -or $Stopped.wpr_stopped -ne $true -or $Saved.nonce -cne $Item.nonce -or $Saved.success -ne $true -or $Saved.steps -ne 3 -or $Capture.method -cne $Prepared.method){throw 'Both stopped recorders and saved successful normal steps required'}
if(Test-Path -LiteralPath (Join-Path $Out 'capture.failed.json')){throw 'Failed capture cannot be exported as acceptance evidence'}
if(@(Get-Process -Name Procmon,Procmon64,Procmon64a -ErrorAction SilentlyContinue).Count){throw 'A Procmon process is active; no export started'}
$Pml=Join-Path $Out 'procmon.pml';$Etl=Join-Path $Out 'kernel.etl'
$Ps=(Get-Item -LiteralPath $Pml).Length;$Es=(Get-Item -LiteralPath $Etl).Length
$Budget=& (Join-Path $PSScriptRoot 'Test-RawBudget.ps1') -PmlBytes $Ps -EtlBytes $Es
if(-not $Budget.Ok){throw ('Raw capture budget exceeded; preserve logs and stop: '+($Budget.Reasons -join '; '))}
$Required=$Budget.RequiredFreeBytes
$MinFree=[long]$Trusted.Configuration.min_free_bytes
if([IO.DriveInfo]::new('C:\').AvailableFreeSpace -lt [Math]::Max($MinFree,$Required)){throw 'Conservative export budget needs max(configured minimum,50xraw+1GiB) free; raw files retained'}
$ExportRoot=Join-Path $Out 'export';if(Test-Path -LiteralPath $ExportRoot){throw 'Export already attempted; no duplicate or overwrite'}
[void](New-Item -ItemType Directory -Path $ExportRoot)
$Procmon=$Trusted.Procmon;$Tracer=$Trusted.Tracerpt
$Before=@{pml=(Get-FileHash -LiteralPath $Pml).Hash;etl=(Get-FileHash -LiteralPath $Etl).Hash}
# Assert-ExportSpace guards the bounded export expansion between native operations.
function Assert-ExportSpace {
 if([IO.DriveInfo]::new('C:\').AvailableFreeSpace -lt ($MinFree*0.8)){throw 'Export free space fell below the budget'}
 if((Get-ChildItem -LiteralPath $Out -File -Recurse|Measure-Object -Property Length -Sum).Sum -gt $Required){throw 'Export expansion exceeded50xraw+1GiB; preserve completed files'}
}
} catch {if(-not $NotSaved){Record-ExportFailure ([string]$_)};throw}
try {
 foreach($Ext in @('csv','xml')){
  Assert-ExportSpace
  if(@(Get-Process -Name Procmon,Procmon64,Procmon64a -ErrorAction SilentlyContinue).Count){throw 'Another Procmon appeared; no export started'}
  $Target=Join-Path $Out ('procmon.'+$Ext);if(Test-Path -LiteralPath $Target){throw 'Export file already exists'}
  $Save=if($Ext -eq 'xml'){'/SaveAs1'}else{'/SaveAs'}
  Write-Host "EXPORT_BEGIN procmon-$Ext $([DateTime]::UtcNow.ToString('o'))"
  $R=Invoke-RecordedNative $Procmon @('/AcceptEula','/Quiet','/Minimized','/NoConnect','/OpenLog',$Pml,$Save,$Target) $ExportRoot ('procmon-'+$Ext) 300
  if($R.Exit -ne 0 -or -not (Test-Path -LiteralPath $Target)){throw 'Procmon export failed'}
 }
 # tracerpt -rts is a dedicated export; summary/report options are deliberately in a second invocation.
 foreach($Mode in @('rawtime','summary')){
  Assert-ExportSpace
  $Xml=Join-Path $Out ('kernel.'+$Mode+'.xml');if(Test-Path -LiteralPath $Xml){throw 'Kernel export file already exists'}
  $Arguments=@($Etl,'-o',$Xml,'-of','XML')
  if($Mode -eq 'rawtime'){$Arguments+='-rts'}else{$Arguments+=@('-summary',(Join-Path $Out 'kernel.summary.private.txt'))}
  Write-Host "EXPORT_BEGIN kernel-$Mode $([DateTime]::UtcNow.ToString('o'))"
  $R=Invoke-RecordedNative $Tracer $Arguments $ExportRoot ('kernel-'+$Mode) 300
  if($R.Exit -ne 0 -or -not (Test-Path -LiteralPath $Xml)){throw 'tracerpt export failed'}
 }
 Assert-ExportSpace
 if((Get-FileHash -LiteralPath $Pml).Hash -ine $Before.pml -or (Get-FileHash -LiteralPath $Etl).Hash -ine $Before.etl){throw 'Raw log changed during export'}
 $Csv=Join-Path $Out 'procmon.csv';$CsvHash=(Get-FileHash -LiteralPath $Csv).Hash.ToLowerInvariant()
 Write-NewJson ($Csv+'.export.json') @{exportAppliedDisplayFilter=$false;csvSha256=$CsvHash;sourcePmlSha256=$Before.pml.ToLowerInvariant();sourceXmlSha256=(Get-FileHash -LiteralPath (Join-Path $Out 'procmon.xml')).Hash.ToLowerInvariant();provenance='Unfiltered OpenLog/SaveAs exports of stopped owned Procmon backing file; CSV/XML identities are independently checked at every ordinal'}
 $Artifacts=@{}
 $RoleFiles=@{pml='procmon.pml';procmon_xml='procmon.xml';etl='kernel.etl';kernel_xml='kernel.rawtime.xml';kernel_summary='kernel.summary.xml';capture='capture.json';volume_before='volume-before.json';volume_after='volume-after.json';pins='pins.json'}
 foreach($Role in $RoleFiles.Keys){$Path=Join-Path $Out $RoleFiles[$Role];$Artifacts[$Role]=@{path=$Path;sha256=(Get-FileHash -LiteralPath $Path).Hash.ToLowerInvariant()}}
 $Evidence=Join-Path $Out 'kernel-evidence.json';Write-NewJson $Evidence @{schema_version=2;artifacts=$Artifacts}
 & (Join-Path $PSScriptRoot 'Export-BookkeepingEvidence.ps1')
 if(-not $?){throw 'Saved-stack/EA evidence export failed'}
 $ReviewArguments=@{Csv=$Csv;ShellPid=$Item.pid;InstallRoot=$Item.install_root;Method=$Capture.method;UserSid=$Item.user_sid;BookkeepingCriterion='windows-bookkeeping-v1';CriterionSha256=$Trusted.Configuration.criterion_sha256;Candidate=$Trusted.Configuration.candidate;OutputDirectory=(Join-Path $Out 'review');BookkeepingEvidence=(Join-Path $Out 'bookkeeping-evidence.json');KernelTraceEvidence=$Evidence}
 if($Capture.method -eq 'nuget-default'){$ReviewArguments.DefaultInstallRoot=$Item.default_install_root}
 & (Join-Path $PSScriptRoot 'Review-InstallerTrace.ps1') @ReviewArguments
 if(-not $?){throw 'Trace reviewer failed'}
 & (Join-Path $PSScriptRoot 'Review-InstallerEnvironment.ps1') -Snapshots (Join-Path $Inputs 'method.environment.private.json') -Method $Capture.method -DefaultInstallRoot $Item.default_install_root
 if(-not $?){throw 'Environment reviewer failed'}
 $Summary=Get-Content -Raw -LiteralPath (Join-Path $Out 'review\procmon.csv.summary.json')|ConvertFrom-Json
 $Environment=Get-Content -Raw -LiteralPath (Join-Path $Inputs 'method.environment.private.json.summary.json')|ConvertFrom-Json
 $Hashes=@(Get-ChildItem -LiteralPath $Out -File -Recurse|Where-Object{$_.Name -notin @('result.json','export.failed.json')}|ForEach-Object{@{file=$_.FullName.Substring($Out.Length+1);sha256=(Get-FileHash -LiteralPath $_.FullName).Hash.ToLowerInvariant();size=$_.Length}})
 Write-NewJson (Join-Path $Out 'result.json') @{schema_version=2;utc=[DateTime]::UtcNow.ToString('o');method=$Capture.method;trace_verdict=$Summary.verdict;environment_verdict=$Environment.verdict;exported_rows=$Summary.exported_rows;bookkeeping_rows=$Summary.bookkeeping_rows;unknown_rows=$Summary.unknown_rows;outside_changes=$Summary.outside_changes;named_pipe_rows=$Summary.named_pipe_rows;pipe_query_rows=$Summary.pipe_query_rows;pipe_query_failure=$(if($Summary.pipe_query_proof){[string]$Summary.pipe_query_proof.failure}else{''});kernel_complete=$Summary.kernel_trace.complete;kernel_failure=$Summary.kernel_trace.failure;fake_podman_bytes=$FakeBytes;captured_commit=$Trusted.Configuration.candidate;criterion_sha256=$Trusted.Configuration.criterion_sha256;all_artifact_hashes=$Hashes;acceptance_record_changed=$false}
 Write-Host "REVIEW_COMPLETE trace=$($Summary.verdict) environment=$($Environment.verdict) unknown=$($Summary.unknown_rows) outside=$($Summary.outside_changes) namedPipe=$($Summary.named_pipe_rows) pipeQueries=$($Summary.pipe_query_rows) fakePodmanBytes=$FakeBytes. No acceptance record changed."
} catch {Record-ExportFailure ([string]$_);throw}
