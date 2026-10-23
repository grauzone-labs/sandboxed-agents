#Requires -Version 7.0
<#Creates v1 saved-stack/EA-query evidence from this run's actual exports; no new bookkeeping exemption.#>
[CmdletBinding()]
param([Parameter(Mandatory)][string]$RunDirectory)
. (Join-Path $PSScriptRoot 'Package.ps1')
Assert-Role $true
$Run=(Resolve-Path -LiteralPath $RunDirectory).ProviderPath
[void](Read-Prepared $Run)
$Csv=Join-Path $Run 'procmon.csv';$Xml=Join-Path $Run 'procmon.xml';$Pml=Join-Path $Run 'procmon.pml'
if(-not ('InstallerKernelTrace' -as [type])){Add-Type -Path (Join-Path $PSScriptRoot 'KernelTrace.cs')}
$Pm=[InstallerKernelTrace]::ReadProcmon($Csv,$Xml)
$Item=Get-Content -Raw -LiteralPath (Join-Path $Run 'item-context.private.json')|ConvertFrom-Json
$Owners=[Collections.Generic.HashSet[uint32]]::new();[void]$Owners.Add([uint32]$Item.pid)
do{
 $Added=$false
 foreach($P in $Pm){
  if($P.Op -eq 'Process Create' -and $Owners.Contains($P.Pid) -and $P.Detail -match '(?:^|,\s*)PID:\s*(\d+)'){if($Owners.Add([uint32]$Matches[1])){$Added=$true}}
  elseif($P.Op -eq 'Process Start' -and $P.Detail -match '(?:^|,\s*)Parent PID:\s*(\d+)' -and $Owners.Contains([uint32]$Matches[1])){if($Owners.Add($P.Pid)){$Added=$true}}
 }
}while($Added)
$Events=@(foreach($P in $Pm|Where-Object{$Owners.Contains($_.Pid) -and $_.Op -in @('WriteFile','SetEAFile','FileSystemControl')}){@{full_ordinal=$P.Ordinal;pid=$P.Pid;operation=$P.Op;path=$P.Path;result=$P.Result;detail=$P.Detail;frames=@($P.Frames|ForEach-Object{@{path=$_}})}})
$Stacks=Join-Path $Run 'bookkeeping-stacks.private.json';Write-NewJson $Stacks @{exported_events=$Pm.Count;events=$Events}
$EaRoot='C:\Program Files\PowerShell\7';$Queries=@();$QueryIndex=0
foreach($Target in ($Pm|Where-Object{$Owners.Contains($_.Pid) -and $_.Op -eq 'SetEAFile'}|Select-Object -ExpandProperty Path -Unique)){
 # v1's exact DLL root remains its seam; other paths stay incomplete rather than broadening the policy.
 if((Split-Path -Parent $Target) -ine $EaRoot -or (Split-Path -Leaf $Target) -notmatch '^[^\\/:]+\.dll$'){continue}
 $QueryIndex++;$Query=Invoke-RecordedNative (Join-Path $env:SystemRoot 'System32\fsutil.exe') @('file','queryEA',$Target) $Run ('ea-query-'+$QueryIndex) 30
 $Names=@(& (Join-Path $PSScriptRoot 'Read-EaNames.ps1') -Text $Query.Text -ExitCode $Query.Exit)
 $Queries+=@{file=(Split-Path -Leaf $Target);queryExit=$Query.Exit;eaNames=$Names;capturedUtc=$Query.EndUtc.ToString('o')}
}
$Ea=Join-Path $Run 'bookkeeping-ea.private.json';Write-NewJson $Ea @($Queries)
$Before=Get-Content -Raw -LiteralPath (Join-Path $Run 'volume-before.json')|ConvertFrom-Json
Write-NewJson (Join-Path $Run 'bookkeeping-evidence.json') @{schema_version=1;csv_sha256=(Get-FileHash -LiteralPath $Csv).Hash.ToLowerInvariant();source_pml_sha256=(Get-FileHash -LiteralPath $Pml).Hash.ToLowerInvariant();source_xml_sha256=(Get-FileHash -LiteralPath $Xml).Hash.ToLowerInvariant();stack_report_file=$Stacks;stack_report_sha256=(Get-FileHash -LiteralPath $Stacks).Hash.ToLowerInvariant();ea_query_file=$Ea;ea_query_sha256=(Get-FileHash -LiteralPath $Ea).Hash.ToLowerInvariant();ea_query_root=$EaRoot;windows_directory=$Before.windows_directory;ntfs_volume_roots=@($Before.drive+'\')}
