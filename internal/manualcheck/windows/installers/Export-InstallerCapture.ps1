#Requires -Version 7.0
<#
Read-only exports and review of one stopped capture run; no capture or installer starts. Each
export is written once. The unfiltered Procmon CSV and stack XML, the raw-time and summary
kernel exports, the saved-stack and extended-attribute evidence, the trace review, and the
environment review end in result.json, which `manual-check installers summarize` reads.
#>
[CmdletBinding()]
param([Parameter(Mandatory)][string]$RunDirectory)
. (Join-Path $PSScriptRoot 'Package.ps1')
Assert-Role $true
$Run=(Resolve-Path -LiteralPath $RunDirectory).ProviderPath
# Record-ExportFailure leaves a durable failure beside the raw files for any error, including the
# checks before the exports; the raw files are never touched.
function Record-ExportFailure([string]$Message) {
 $Marker=Join-Path $Run 'export.failed.json'
 if(-not (Test-Path -LiteralPath $Marker)){Write-NewJson $Marker @{utc=[DateTime]::UtcNow.ToString('o');failure=$Message;raw_logs_preserved=$true;acceptance_changed=$false}}
}
try {
$Prepared=Read-Prepared $Run -Signatures
$Capture=Get-Content -Raw -LiteralPath (Join-Path $Run 'capture.json')|ConvertFrom-Json
$Stopped=Get-Content -Raw -LiteralPath (Join-Path $Run 'capture.stopped.json')|ConvertFrom-Json
$Item=Get-Content -Raw -LiteralPath (Join-Path $Run 'item-context.private.json')|ConvertFrom-Json
$Saved=Get-Content -Raw -LiteralPath (Join-Path $Run 'item.saved.json')|ConvertFrom-Json
if($Stopped.nonce -cne $Item.nonce -or $Stopped.procmon_stopped -ne $true -or $Stopped.wpr_stopped -ne $true -or $Saved.nonce -cne $Item.nonce -or $Saved.success -ne $true -or $Saved.steps -ne 3 -or $Capture.method -cne $Prepared.method){throw 'Both stopped recorders and saved successful normal steps required'}
if(Test-Path -LiteralPath (Join-Path $Run 'capture.failed.json')){throw 'Failed capture cannot be exported as acceptance evidence'}
if(@(Get-Process -Name Procmon,Procmon64,Procmon64a -ErrorAction SilentlyContinue).Count){throw 'A Procmon process is active; no export started'}
$Pml=Join-Path $Run 'procmon.pml';$Etl=Join-Path $Run 'kernel.etl'
$Ps=(Get-Item -LiteralPath $Pml).Length;$Es=(Get-Item -LiteralPath $Etl).Length
$Budget=& (Join-Path $PSScriptRoot 'Test-RawBudget.ps1') -PmlBytes $Ps -EtlBytes $Es
if(-not $Budget.Ok){throw ('Raw capture budget exceeded; preserve logs and stop: '+($Budget.Reasons -join '; '))}
$Required=$Budget.RequiredFreeBytes
$MinFree=[long]$script:Configuration.min_free_bytes
if([IO.DriveInfo]::new('C:\').AvailableFreeSpace -lt [Math]::Max($MinFree,$Required)){throw 'Conservative export budget needs max(configured minimum,50xraw+1GiB) free; raw files retained'}
$ExportRoot=Join-Path $Run 'export';if(Test-Path -LiteralPath $ExportRoot){throw 'Export already attempted; no duplicate or overwrite'}
[void](New-Item -ItemType Directory -Path $ExportRoot)
$Procmon=$script:Configuration.native_tools.procmon.path;$Tracer=$script:Configuration.native_tools.tracerpt.path
$Before=@{pml=(Get-FileHash -LiteralPath $Pml).Hash;etl=(Get-FileHash -LiteralPath $Etl).Hash}
# Assert-ExportSpace guards the bounded export expansion between native operations.
function Assert-ExportSpace {
 if([IO.DriveInfo]::new('C:\').AvailableFreeSpace -lt ($MinFree*0.8)){throw 'Export free space fell below the budget'}
 if((Get-ChildItem -LiteralPath $Run -File -Recurse|Measure-Object -Property Length -Sum).Sum -gt $Required){throw 'Export expansion exceeded50xraw+1GiB; preserve completed files'}
}
} catch {Record-ExportFailure ([string]$_);throw}
try {
 foreach($Ext in @('csv','xml')){
  Assert-ExportSpace
  if(@(Get-Process -Name Procmon,Procmon64,Procmon64a -ErrorAction SilentlyContinue).Count){throw 'Another Procmon appeared; no export started'}
  $Target=Join-Path $Run ('procmon.'+$Ext);if(Test-Path -LiteralPath $Target){throw 'Export file already exists'}
  $Save=if($Ext -eq 'xml'){'/SaveAs1'}else{'/SaveAs'}
  Write-Host "EXPORT_BEGIN procmon-$Ext $([DateTime]::UtcNow.ToString('o'))"
  $R=Invoke-RecordedNative $Procmon @('/AcceptEula','/Quiet','/Minimized','/NoConnect','/OpenLog',$Pml,$Save,$Target) $ExportRoot ('procmon-'+$Ext) 300
  if($R.Exit -ne 0 -or -not (Test-Path -LiteralPath $Target)){throw 'Procmon export failed'}
 }
 # tracerpt -rts is a dedicated export; summary/report options are deliberately in a second invocation.
 foreach($Mode in @('rawtime','summary')){
  Assert-ExportSpace
  $Xml=Join-Path $Run ('kernel.'+$Mode+'.xml');if(Test-Path -LiteralPath $Xml){throw 'Kernel export file already exists'}
  $Arguments=@($Etl,'-o',$Xml,'-of','XML')
  if($Mode -eq 'rawtime'){$Arguments+='-rts'}else{$Arguments+=@('-summary',(Join-Path $Run 'kernel.summary.private.txt'))}
  Write-Host "EXPORT_BEGIN kernel-$Mode $([DateTime]::UtcNow.ToString('o'))"
  $R=Invoke-RecordedNative $Tracer $Arguments $ExportRoot ('kernel-'+$Mode) 300
  if($R.Exit -ne 0 -or -not (Test-Path -LiteralPath $Xml)){throw 'tracerpt export failed'}
 }
 Assert-ExportSpace
 if((Get-FileHash -LiteralPath $Pml).Hash -ine $Before.pml -or (Get-FileHash -LiteralPath $Etl).Hash -ine $Before.etl){throw 'Raw log changed during export'}
 $Csv=Join-Path $Run 'procmon.csv';$CsvHash=(Get-FileHash -LiteralPath $Csv).Hash.ToLowerInvariant()
 Write-NewJson ($Csv+'.export.json') @{exportAppliedDisplayFilter=$false;csvSha256=$CsvHash;sourcePmlSha256=$Before.pml.ToLowerInvariant();sourceXmlSha256=(Get-FileHash -LiteralPath (Join-Path $Run 'procmon.xml')).Hash.ToLowerInvariant();provenance='Unfiltered OpenLog/SaveAs exports of stopped owned Procmon backing file; CSV/XML identities are independently checked at every ordinal'}
 $Artifacts=@{}
 $RoleFiles=@{pml='procmon.pml';procmon_xml='procmon.xml';etl='kernel.etl';kernel_xml='kernel.rawtime.xml';kernel_summary='kernel.summary.xml';capture='capture.json';volume_before='volume-before.json';volume_after='volume-after.json';pins='pins.json'}
 foreach($Role in $RoleFiles.Keys){$Path=Join-Path $Run $RoleFiles[$Role];$Artifacts[$Role]=@{path=$Path;sha256=(Get-FileHash -LiteralPath $Path).Hash.ToLowerInvariant()}}
 $Evidence=Join-Path $Run 'kernel-evidence.json';Write-NewJson $Evidence @{schema_version=2;artifacts=$Artifacts}
 & (Join-Path $PSScriptRoot 'Export-BookkeepingEvidence.ps1') -RunDirectory $Run
 if(-not $?){throw 'Saved-stack/EA evidence export failed'}
 $ReviewArguments=@{Csv=$Csv;ShellPid=$Item.pid;InstallRoot=$Item.install_root;Method=$Capture.method;UserSid=$Item.user_sid;BookkeepingCriterion='windows-bookkeeping-v1';CriterionSha256=$script:Configuration.criterion_sha256;Candidate=$script:Configuration.candidate;OutputDirectory=(Join-Path $Run 'review');BookkeepingEvidence=(Join-Path $Run 'bookkeeping-evidence.json');KernelTraceEvidence=$Evidence}
 if($Capture.method -eq 'nuget-default'){$ReviewArguments.DefaultInstallRoot=$Item.default_install_root}
 & (Join-Path $PSScriptRoot 'Review-InstallerTrace.ps1') @ReviewArguments
 if(-not $?){throw 'Trace reviewer failed'}
 & (Join-Path $PSScriptRoot 'Review-InstallerEnvironment.ps1') -Snapshots (Join-Path $Run 'method.environment.private.json') -Method $Capture.method -DefaultInstallRoot $Item.default_install_root
 if(-not $?){throw 'Environment reviewer failed'}
 $Summary=Get-Content -Raw -LiteralPath (Join-Path $Run 'review\procmon.csv.summary.json')|ConvertFrom-Json
 $Environment=Get-Content -Raw -LiteralPath (Join-Path $Run 'method.environment.private.json.summary.json')|ConvertFrom-Json
 $FakeBytes=if(Test-Path -LiteralPath $Prepared.fake_calls){(Get-Item -LiteralPath $Prepared.fake_calls).Length}else{0}
 $Hashes=@(Get-ChildItem -LiteralPath $Run -File -Recurse|Where-Object{$_.Name -notin @('result.json','export.failed.json')}|ForEach-Object{@{file=$_.FullName.Substring($Run.Length+1);sha256=(Get-FileHash -LiteralPath $_.FullName).Hash.ToLowerInvariant();size=$_.Length}})
 Write-NewJson (Join-Path $Run 'result.json') @{schema_version=2;utc=[DateTime]::UtcNow.ToString('o');method=$Capture.method;trace_verdict=$Summary.verdict;environment_verdict=$Environment.verdict;exported_rows=$Summary.exported_rows;bookkeeping_rows=$Summary.bookkeeping_rows;unknown_rows=$Summary.unknown_rows;outside_changes=$Summary.outside_changes;kernel_complete=$Summary.kernel_trace.complete;kernel_failure=$Summary.kernel_trace.failure;fake_podman_bytes=$FakeBytes;captured_commit=$script:Configuration.candidate;criterion_sha256=$script:Configuration.criterion_sha256;all_artifact_hashes=$Hashes;acceptance_record_changed=$false}
 Write-Host "REVIEW_COMPLETE trace=$($Summary.verdict) environment=$($Environment.verdict) unknown=$($Summary.unknown_rows) outside=$($Summary.outside_changes) fakePodmanBytes=$FakeBytes. No acceptance record changed."
} catch {Record-ExportFailure ([string]$_);throw}
