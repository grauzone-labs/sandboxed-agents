#Requires -Version 7.0
<#
The normal, non-elevated item shell of one capture run. `manual-check installers prepare` opens it
with the prepared environment; it warms up what the steps need before any capture, then exposes
Begin-InstallerMethod, Invoke-InstallerStep, and Save-InstallerMethod. The elevated observer owns
the recorders and the Procmon process; this shell only checks the observer's ready record, which
is all a normal process can see of an elevated one.
#>
if(-not $env:CHK_V2_RUN -or -not $env:CHK_INSTALLER_CONFIGURATION){throw 'Start with manual-check installers prepare'}
. (Join-Path $PSScriptRoot 'Package.ps1')
Assert-Role $false
$global:V2Run=$env:CHK_V2_RUN;$global:V2Tools=$PSScriptRoot
$global:V2=Read-Prepared $V2Run
if($V2.configuration.path -ine $env:CHK_INSTALLER_CONFIGURATION){throw 'The item shell environment names another configuration'}
Set-PSReadLineOption -HistorySaveStyle SaveNothing
$global:V2Step=0;$global:V2Begun=$false;$global:V2Failed=$false;$global:V2Saved=$false;$global:V2Snapshots=[ordered]@{};$global:V2Results=@()
Set-Location -LiteralPath $V2.install_root
# Get-ItemEnvironment snapshots the raw user-environment values in memory.
function global:Get-ItemEnvironment {
 $Key=Get-Item HKCU:\Environment
 return @($Key.GetValueNames()|Sort-Object|ForEach-Object{"$_ [$($Key.GetValueKind($_))] = $($Key.GetValue($_,'','DoNotExpandEnvironmentNames'))"})
}
Get-ItemEnvironment|Out-Null
& (Join-Path $V2.node_directory 'node.exe') --version
if($LASTEXITCODE -ne 0){throw 'Node warm-up failed'}
& (Join-Path $V2.node_directory 'npm.cmd') --version --cache "$($V2.install_root)\cache" --logs-dir "$($V2.install_root)\logs"
if($LASTEXITCODE -ne 0){throw 'npm warm-up failed'}
Get-Command Copy-Item,Remove-Item,Set-Content,Get-Item,Get-ChildItem,Unblock-File,Compare-Object,Get-FileHash|Out-Null
$global:V2NuGetTools=Join-Path $V2.install_root 'package\tools'
if($V2.method -like 'nuget-*'){
 # The package was extracted by the preparation; unblocking happens here, before any capture.
 Get-ChildItem -LiteralPath $V2NuGetTools -Filter *.ps1|Unblock-File
 Get-Command (Join-Path $V2NuGetTools 'install-command.ps1'),(Join-Path $V2NuGetTools 'remove-command.ps1')|Out-Null
}
$Deadline=(Get-Date).AddSeconds(60)
while(-not (Test-Path -LiteralPath $env:PSModuleAnalysisCachePath) -and (Get-Date) -lt $Deadline){Start-Sleep -Seconds 2}
Start-Sleep -Seconds 10
$Shell=Get-Process -Id $PID
$Context=@{schema_version=2;method=$V2.method;run_id=$V2.run_id;nonce=[Guid]::NewGuid().ToString('N');pid=$PID;path=$Shell.Path;start_ticks=$Shell.StartTime.ToUniversalTime().Ticks;command_line=[Environment]::CommandLine;non_elevated=$true;install_root=$V2.install_root;completion_file=(Join-Path $V2.install_root 'logs\method-complete.json');user_sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value;default_install_root=(Join-Path $env:LOCALAPPDATA 'Programs\sandboxed-agents')}
Write-NewJson (Join-Path $V2Run 'item-context.private.json') $Context
$global:V2Context=[pscustomobject]$Context
# Begin-InstallerMethod checks the real observer's ready record and takes the initial in-memory snapshot.
function global:Begin-InstallerMethod {
 if($V2Begun -or $V2Failed){throw 'Method already begun/failed; no repeated steps'}
 $Ready=Get-Content -Raw -LiteralPath (Join-Path $V2Run 'capture.ready.json')|ConvertFrom-Json
 [void](& (Join-Path $V2Tools 'Test-InstallerReady.ps1') -Record $Ready -Context $V2Context)
 # The elevated observer owns and monitors Procmon; a normal item shell cannot query its protected metadata.
 $global:V2Snapshots['start']=@(Get-ItemEnvironment);$global:V2Begun=$true
 Write-Host "READY $($V2.method). Run each install/reinstall/remove step separately."
}
# Invoke-InstallerStep runs one approved operation and records exact exits and user-environment snapshots in memory.
function global:Invoke-InstallerStep([ValidateSet('install','reinstall','remove')][string]$Step) {
 if(-not $V2Begun -or $V2Failed -or $V2Step -ge 3 -or $Step -cne @('install','reinstall','remove')[$V2Step]){throw 'Out-of-order/repeated step; nothing ran'}
 $Ready=Get-Content -Raw -LiteralPath (Join-Path $V2Run 'capture.ready.json')|ConvertFrom-Json
 [void](& (Join-Path $V2Tools 'Test-InstallerReady.ps1') -Record $Ready -Context $V2Context)
 if(Test-Path -LiteralPath (Join-Path $V2Run 'capture.stopped.json')){throw 'Capture already stopped'}
 $Result=[ordered]@{step=$Step;success=$false;native_exits=@();error=''}
 try {
  if($V2.method -eq 'binary') {
   $Target=Join-Path $V2.install_root 'bin\sandboxed-agents.exe'
   if($Step -eq 'remove'){Remove-Item -LiteralPath $Target}
   else {
    $Source=if($Step -eq 'install'){$V2.inputs.binary.path}else{$V2.inputs.second_binary.path}
    Copy-Item -LiteralPath $Source -Destination $Target -Force:($Step -eq 'reinstall')
    & $Target version;$Result.native_exits+=@($LASTEXITCODE);if($LASTEXITCODE -ne 0){throw 'Binary version command failed'}
   }
  } elseif($V2.method -eq 'npm') {
   $Npm=Join-Path $V2.node_directory 'npm.cmd'
   if($Step -eq 'remove'){& $Npm uninstall -g --prefix "$($V2.install_root)\prefix" --cache "$($V2.install_root)\cache" --logs-dir "$($V2.install_root)\logs" sandboxed-agents}
   else{& $Npm install --offline --no-audit --no-fund -g --prefix "$($V2.install_root)\prefix" --cache "$($V2.install_root)\cache" --logs-dir "$($V2.install_root)\logs" $V2.inputs.npm_package.path}
   $Result.native_exits+=@($LASTEXITCODE);if($LASTEXITCODE -ne 0){throw 'npm operation failed'}
   if($Step -ne 'remove'){& "$($V2.install_root)\prefix\sandboxed-agents.cmd" version;$Result.native_exits+=@($LASTEXITCODE);if($LASTEXITCODE -ne 0){throw 'npm version command failed'}}
  } elseif($V2.method -eq 'nuget-root') {
   # The scripts run inside this shell with the call operator; no new PowerShell starts in a capture.
   $Script=if($Step -eq 'remove'){'remove-command.ps1'}else{'install-command.ps1'}
   & (Join-Path $V2NuGetTools $Script) -InstallDirectory (Join-Path $V2.install_root 'nuget') -NoPathUpdate
   if(-not $?){throw "NuGet $Script failed"}
  } elseif($V2.method -eq 'nuget-default') {
   $Script=if($Step -eq 'remove'){'remove-command.ps1'}else{'install-command.ps1'}
   & (Join-Path $V2NuGetTools $Script)
   if(-not $?){throw "NuGet $Script failed"}
  } else {throw 'Unknown method'}
  $global:V2Snapshots['after-'+$Step]=@(Get-ItemEnvironment);$Result.success=$true;$global:V2Step++
 } catch {$global:V2Failed=$true;$Result.error=[string]$_;throw} finally {
  $global:V2Results+=@([pscustomobject]$Result)
  if($V2Step -eq 3 -or $V2Failed){Write-NewJson $V2Context.completion_file @{nonce=$V2Context.nonce;pid=$PID;success=(-not $V2Failed);steps=$V2Step}}
 }
 Write-Host "STEP_COMPLETE $Step; success=True"
}
# Save-InstallerMethod writes snapshots only after the exact observer proves that both recorders stopped.
function global:Save-InstallerMethod {
 if($V2Saved){throw 'Method already saved'}
 $Stopped=Get-Content -Raw -LiteralPath (Join-Path $V2Run 'capture.stopped.json')|ConvertFrom-Json
 if($Stopped.nonce -cne $V2Context.nonce -or $Stopped.procmon_stopped -ne $true -or $Stopped.wpr_stopped -ne $true){throw 'Both owned recorders not proven stopped'}
 if(@(Get-Process -Name Procmon,Procmon64,Procmon64a -ErrorAction SilentlyContinue).Count){throw 'A Procmon process is still running'}
 Write-NewJson (Join-Path $V2Run 'method.steps.private.json') @($V2Results)
 Write-NewJson (Join-Path $V2Run 'method.environment.private.json') $V2Snapshots
 $global:V2Saved=$true
 Write-NewJson (Join-Path $V2Run 'item.saved.json') @{nonce=$V2Context.nonce;success=(-not $V2Failed);steps=$V2Step;pid=$PID}
 Write-Host 'SAVED. Elevated observer now exports and reviews automatically. Keep this shell open.'
}
Write-Host "ITEM_SHELL_READY method=$($V2.method) RUN_DIRECTORY=$V2Run"
Write-Host 'Launch the elevated observer now. After CAPTURE_READY, run Begin-InstallerMethod, then each of the three steps separately.'
