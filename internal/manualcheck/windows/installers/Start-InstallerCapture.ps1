#Requires -Version 7.0
<#
The elevated observer of one capture run. A person starts it once in an interactive elevated
PowerShell 7 of a separate administrator account after `manual-check installers prepare` opened the
item shell. Before CAPTURE_READY it copies every pinned package file, the configuration, and Procmon
into an admin-only trusted snapshot and creates the run's protected output directory. Everything it
executes from then on, including the export after the workload, comes from that snapshot with the
snapshot as working directory, and everything it writes goes into the output directory, never into
the run directory. WPR starts before Procmon; the pins of every snapshot file, test, profile, and
native tool are written before the recorders start; no installer runs here. It owns the Procmon
process it starts, proves its exact identity before stopping it, and stops only its own named WPR
instance.
#>
[CmdletBinding()]
param([Parameter(Mandatory)][string]$RunDirectory)
. (Join-Path $PSScriptRoot 'Package.ps1')
Assert-Role $true
# System32 is the working directory until the snapshot exists, then the snapshot itself.
Set-ProtectedLocation (Get-SystemDirectory)
if([Console]::IsInputRedirected){throw 'Use an interactive elevated observer terminal'}
$Run=(Resolve-Path -LiteralPath $RunDirectory).ProviderPath
$Prepared=Read-Prepared $Run -Signatures
$Item=Get-Content -Raw -LiteralPath (Join-Path $Run 'item-context.private.json')|ConvertFrom-Json
if($Item.method -cne $Prepared.method -or $Item.run_id -cne $Prepared.run_id -or $Item.non_elevated -ne $true -or $Item.install_root -ine $Prepared.install_root){throw 'Prepared normal item context mismatch'}
[void](Get-OwnedProcess $Item)
$ItemSid=Assert-SeparateObserver $Item
if(@(Get-Process -Name Procmon,Procmon64,Procmon64a -ErrorAction SilentlyContinue).Count){throw 'A Procmon process already exists; nothing started or signaled'}
if(Test-Path -LiteralPath $Item.completion_file){throw 'Method was already performed; no capture started'}
$MinFree=[long]$script:Configuration.min_free_bytes
if([IO.DriveInfo]::new('C:\').AvailableFreeSpace -lt $MinFree){throw "Need $MinFree bytes free capture/export headroom"}
# The trusted snapshot is taken here, before CAPTURE_READY releases the workload. From this line on,
# no helper, configuration, or executable path comes from the package directory or the run directory,
# and every file this observer writes lies in the protected output directory, which a new run creates
# and never reuses.
$Trusted=New-TrustedSnapshot $Prepared $Item $ItemSid $Run $PSScriptRoot
Set-ProtectedLocation $Trusted.Root
$Out=$Trusted.OutputDirectory
Write-Host "TRUSTED_SNAPSHOT=$($Trusted.Root)"
Write-Host "OUTPUT_DIRECTORY=$Out"
$Observer=Join-Path $Out 'observer';[void](New-Item -ItemType Directory -Path $Observer)
$Procmon=$Trusted.Procmon;$Wpr=$Trusted.Wpr
$Instance='manual-check-'+$Prepared.run_id;$Profile=Join-Path $Trusted.Root 'InstallerJournal.wprp'
$Pml=Join-Path $Out 'procmon.pml';$Etl=Join-Path $Out 'kernel.etl'
$PinFiles=@(foreach($F in $Trusted.Configuration.package_files){@{path=(Join-Path $Trusted.Root $F.path);sha256=$F.sha256}})
$PinFiles+=@(@{path=$Trusted.Procmon;sha256=$Trusted.ProcmonSha256},@{path=$Trusted.Wpr;sha256=$Trusted.WprSha256},@{path=$Trusted.Tracerpt;sha256=$Trusted.TracerptSha256})
Write-NewJson (Join-Path $Out 'pins.json') @{schema_version=2;created_utc=[DateTime]::UtcNow.ToString('o');files=$PinFiles;configuration_sha256=$Trusted.ConfigurationSha256;trusted_snapshot=$Trusted.Root}
$Before=Save-Volume (Join-Path $Out 'volume-before.json')
$Capture=$null;$Identity=$null;$PmStopped=$false;$WprStarted=$false;$WprStopped=$false;$PmStopAttempt=$false;$WprStopAttempt=$false;$Failure='';$Confirmed=$false
$Deadline=[DateTime]::UtcNow.AddMinutes(5)
# Assert-CaptureBudget enforces duration, Procmon raw size and disk headroom while preserving owned state.
function Assert-CaptureBudget {
 if([DateTime]::UtcNow -gt $Deadline){throw 'Five-minute capture budget exceeded'}
 if([IO.DriveInfo]::new('C:\').AvailableFreeSpace -lt ($MinFree*0.8)){throw 'Free disk fell below the capture budget'}
 if((Test-Path -LiteralPath $Pml) -and (Get-Item -LiteralPath $Pml).Length -gt 256MB){throw 'Procmon raw capture exceeded256MiB'}
 if($Capture -and $Capture.HasExited){throw 'Owned Procmon exited before method completion'}
}
# Stop-OwnedProcmon signals only after revalidating the exact sole owned instance; no name-based kill is used.
function Stop-OwnedProcmon {
 if(-not $Capture){return}
 if($Capture.HasExited){$script:PmStopped=$true;return}
 if($script:PmStopAttempt){throw 'Procmon stop was already attempted'}
 [void](Get-OwnedProcess $Identity)
 if(@(Get-Process -Name Procmon,Procmon64,Procmon64a -ErrorAction SilentlyContinue|Where-Object{$_.Id -ne $Identity.pid}).Count){throw 'Another Procmon exists; no terminate command sent'}
 $script:PmStopAttempt=$true
 $Stop=Invoke-RecordedNative $Procmon @('/AcceptEula','/Quiet','/Terminate') $Observer 'procmon-stop' 30
 if($Stop.Exit -ne 0 -or -not $Capture.WaitForExit(30000)){throw 'Owned Procmon did not exit; no export permitted'}
 $script:PmStopped=$true
}
try {
 $Pre=Invoke-RecordedNative $Wpr @('-status','-instancename',$Instance) $Observer 'wpr-pre-status' 30
 if(-not (& (Join-Path $Trusted.Root 'Test-WprInactiveStatus.ps1') -ExitCode $Pre.Exit -Stdout $Pre.Text -Stderr $Pre.ErrorText)){throw 'Named WPR instance not proven inactive; nothing started'}
 # The completed WPR start and collector checks precede the actual Procmon process creation.
 $Start=Invoke-RecordedNative $Wpr @('-start',($Profile+'!InstallerJournal'),'-instancename',$Instance) $Observer 'wpr-start' 60
 if($Start.Exit -ne 0){throw 'WPR start failed; no Procmon/workload launched'};$WprStarted=$true
 $Collectors=Invoke-RecordedNative $Wpr @('-status','collectors','-details','-instancename',$Instance) $Observer 'wpr-collectors' 30
 $Profiles=Invoke-RecordedNative $Wpr @('-status','profiles','-instancename',$Instance) $Observer 'wpr-profiles' 30
 if($Collectors.Exit -ne 0 -or $Profiles.Exit -ne 0 -or $Collectors.Text -notmatch '\bFileWrite\b' -or $Collectors.Text -notmatch '\bProcessThread\b' -or $Collectors.Text -notmatch '\bLoader\b' -or $Collectors.Text -notmatch '\bFileIO\b' -or $Profiles.Text -notmatch '\bInstallerJournal\.Verbose\.Memory\b'){throw 'Required WPR profile/events/stacks not active'}
 $Info=New-ProtectedStartInfo $Procmon
 foreach($A in @('/AcceptEula','/Quiet','/NoConnect','/BackingFile',$Pml)){[void]$Info.ArgumentList.Add($A)}
 $Capture=[Diagnostics.Process]::Start($Info);$Ticks=$Capture.StartTime.ToUniversalTime().Ticks
 $Cim=Get-CimInstance Win32_Process -Filter "ProcessId=$($Capture.Id)"
 $Identity=[pscustomobject]@{pid=$Capture.Id;path=$Procmon;start_ticks=$Ticks;command_line=$Cim.CommandLine}
 [void](Get-OwnedProcess $Identity)
 Write-NewJson (Join-Path $Observer 'procmon-start.launch.json') $Identity
 Write-Host 'In this Procmon window: Reset filters; Enable Advanced Output OFF; Drop Filtered Events OFF.'
 Write-Host 'Enable File System, Registry and Process/Thread activity. Enable Date & Time, Thread ID and Duration columns.'
 Write-Host 'Keep Process Name, PID, Operation, Path, Result and Detail. Start capture (Ctrl+E) if paused; confirm counter increases.'
 Write-Host 'Press Enter here after visual confirmation, or Escape to stop. Complete all three separate normal-shell steps promptly within the five-minute raw budget.'
 while(-not $Confirmed){
  Assert-CaptureBudget
  if([Console]::KeyAvailable){$Key=[Console]::ReadKey($true);if($Key.Key -eq [ConsoleKey]::Enter){$Confirmed=$true};if($Key.Key -eq [ConsoleKey]::Escape){throw 'Human canceled before method release'}}
  Start-Sleep -Milliseconds 200
 }
 [void](Get-OwnedProcess $Item);[void](Get-OwnedProcess $Identity)
 $Ready=@{schema_version=2;nonce=$Item.nonce;method=$Item.method;item_pid=$Item.pid;elevated_observer=$true;wpr=@{active=$true;start_exit=$Start.Exit;collectors_exit=$Collectors.Exit;profiles_exit=$Profiles.Exit;stacks=@('FileWrite')};procmon=@{pid=$Identity.pid;path=$Identity.path;start_ticks=$Identity.start_ticks;command_line=$Identity.command_line;advanced_output=$false;drop_filtered_events=$false;confirmed_by='human-elevated-observer';activity=@('File System','Registry','Process/Thread');columns=@('Date & Time','Thread ID','Duration')}}
 [void](& (Join-Path $Trusted.Root 'Test-InstallerReady.ps1') -Record ([pscustomobject]$Ready) -Context $Item)
 Write-NewJson (Join-Path $Out 'capture.ready.json') $Ready
 Write-Host 'CAPTURE_READY. In normal item shell: Begin-InstallerMethod, then Invoke-InstallerStep install/reinstall/remove, separately.'
 # What the item shell writes after this point is read only through Read-BoundInput; a file it is
 # still writing counts as not there yet, within the capture budget.
 while($null -eq ($DoneBytes=Read-BoundInput $Trusted.Bound.install_logs 'method-complete.json' -PendingWhileWritten)){Assert-CaptureBudget;Start-Sleep -Milliseconds 500}
 $Done=[Text.Encoding]::UTF8.GetString($DoneBytes)|ConvertFrom-Json
 if($Done.nonce -cne $Item.nonce -or $Done.pid -ne $Item.pid -or $Done.steps -ne 3 -or $Done.success -ne $true){throw 'Method did not complete three successful steps'}
} catch {$Failure=[string]$_} finally {
 try{Stop-OwnedProcmon}catch{$Failure+='; owned Procmon stop: '+[string]$_}
 if($WprStarted -and -not $WprStopAttempt){
  $WprStopAttempt=$true
  try {
   $WprStop=Invoke-RecordedNative $Wpr @('-stop',$Etl,'manual-check installer evidence','-skipPdbGen','-instancename',$Instance) $Observer 'wpr-stop' 120
   if($WprStop.Exit -ne 0){throw 'Named WPR stop failed'};$WprStopped=$true
  } catch {$Failure+='; owned WPR stop: '+[string]$_}
 }
}
if($Failure){Write-NewJson (Join-Path $Out 'capture.failed.json') @{failure=$Failure;nonce=$Item.nonce;procmon_stopped=$PmStopped;wpr_stopped=$WprStopped;acceptance_changed=$false};throw $Failure}
$Post=Invoke-RecordedNative $Wpr @('-status','-instancename',$Instance) $Observer 'wpr-post-status' 30
if(-not (& (Join-Path $Trusted.Root 'Test-WprInactiveStatus.ps1') -ExitCode $Post.Exit -Stdout $Post.Text -Stderr $Post.ErrorText)){throw 'Named WPR did not become inactive; preserve raw files'}
$After=Save-Volume (Join-Path $Out 'volume-after.json')
foreach($K in @('drive','nt_device','filesystem','drive_type','windows_directory')){if($Before.$K -ine $After.$K){throw 'Before/after volume mapping changed'}}
Write-NewJson (Join-Path $Out 'capture.json') @{schema_version=2;method=$Item.method;criterion_sha256=$Trusted.Configuration.criterion_sha256;captured_commit=$Trusted.Configuration.candidate;trusted_snapshot=$Trusted.Root;profile_path=$Profile;profile_sha256=(Get-FileHash -LiteralPath $Profile).Hash.ToLowerInvariant();wpr_instance=$Instance;wpr_start_utc=$Start.EndUtc.ToString('o');wpr_stop_utc=$WprStop.EndUtc.ToString('o');procmon=@{pid=$Identity.pid;path=$Procmon;backing_file=$Pml;command_line=$Identity.command_line;start_utc=$Capture.StartTime.ToUniversalTime().ToString('o');end_utc=$Capture.ExitTime.ToUniversalTime().ToString('o')};settings=@{drop_filtered_events=$false;advanced_output=$false;export_applied_display_filter=$false;human_confirmed=$Confirmed;activity=@('File System','Registry','Process/Thread')}}
Write-NewJson (Join-Path $Out 'capture.stopped.json') @{nonce=$Item.nonce;procmon_stopped=$PmStopped;wpr_stopped=$WprStopped;utc=[DateTime]::UtcNow.ToString('o')}
Write-Host 'CAPTURE_STOPPED. Run Save-InstallerMethod in the normal item shell now.'
$SaveDeadline=[DateTime]::UtcNow.AddMinutes(3)
while($null -eq (Read-BoundInput $Trusted.Bound.run 'item.saved.json' -PendingWhileWritten)){if([DateTime]::UtcNow -gt $SaveDeadline){throw "Item data save pending; raw files preserved; after saving, run pwsh -NoProfile -File '$(Join-Path $Trusted.Root 'Export-InstallerCapture.ps1')' -RunDirectory '$Run' in an elevated terminal of the observer account"};Start-Sleep -Milliseconds 500}
& (Join-Path $Trusted.Root 'Export-InstallerCapture.ps1') -RunDirectory $Run
