#Requires -Version 7.0
<#Read the per-method EnvSnap JSON exported after capture; never print values.#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Snapshots,
    [Parameter(Mandatory)][ValidateSet('binary','npm','nuget-root','nuget-default')][string]$Method,
    [string]$DefaultInstallRoot = "$env:LOCALAPPDATA\Programs\sandboxed-agents"
)
$ErrorActionPreference='Stop'
$Stages=@('start','after-install','after-reinstall','after-remove')
$Path=(Resolve-Path -LiteralPath $Snapshots).ProviderPath
$Data=Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable
$Problems=[Collections.Generic.List[string]]::new()
foreach($Stage in $Stages){if(-not $Data.Contains($Stage)){throw "INCOMPLETE: missing $Stage snapshot"}}
# Test-ExactLines compares original snapshot strings without expanding values.
function Test-ExactLines($Left,$Right){
    $A=@($Left);$B=@($Right)
    if($A.Count -ne $B.Count){return $false}
    for($Index=0;$Index -lt $A.Count;$Index++){if($A[$Index] -cne $B[$Index]){return $false}}
    return $true
}
if($Method -ne 'nuget-default'){
    foreach($Stage in $Stages[1..3]){if(-not (Test-ExactLines $Data.start $Data[$Stage])){$Problems.Add("Environment changed at $Stage")}}
}else{
    $Records=@{}
    foreach($Stage in $Stages){
        $Lines=@($Data[$Stage])
        $PathLines=@($Lines | Where-Object {$_ -match '^Path \[[^\]]+\] = '})
        if($PathLines.Count -gt 1){throw "INCOMPLETE: duplicate Path snapshot at $Stage"}
        $Other=@($Lines | Where-Object {$_ -notmatch '^Path \[[^\]]+\] = '})
        if($PathLines.Count){
            if($PathLines[0] -notmatch '(?s)^Path \[([^\]]+)\] = (.*)$'){throw 'Unrecognized Path snapshot'}
            $Records[$Stage]=@{Kind=$Matches[1];Value=$Matches[2];Other=$Other;Present=$true}
        }else{$Records[$Stage]=@{Kind=$null;Value='';Other=$Other;Present=$false}}
    }
    foreach($Stage in $Stages[1..3]){if(-not (Test-ExactLines $Records.start.Other $Records[$Stage].Other)){$Problems.Add("A non-Path value changed at $Stage")}}
    if(-not (Test-ExactLines $Data.start $Data['after-remove'])){$Problems.Add('Final environment does not exactly equal start')}
    if(-not (Test-ExactLines $Data['after-install'] $Data['after-reinstall'])){$Problems.Add('Reinstall changed the environment')}
    $Before=$Records.start;$After=$Records['after-install']
    if(-not $After.Present){$Problems.Add('Install did not set Path')}
    if($Before.Present -and $Before.Kind -cne $After.Kind){$Problems.Add('Path registry kind changed')}
    $Parts=@($After.Value.Split(';',[StringSplitOptions]::None))
    $MatchesAt=@(for($Index=0;$Index -lt $Parts.Count;$Index++){if($Parts[$Index] -ieq $DefaultInstallRoot){$Index}})
    if($MatchesAt.Count -ne 1){$Problems.Add('Installed Path does not contain exactly one default directory entry')}
    else{
        $Remaining=@(for($Index=0;$Index -lt $Parts.Count;$Index++){if($Index -ne $MatchesAt[0]){$Parts[$Index]}})
        if([string]::Join(';',$Remaining) -cne $Before.Value){$Problems.Add('Original raw Path entries or separators changed')}
    }
}
$Summary=[ordered]@{method=$Method;verdict=$(if($Problems.Count){'FAIL'}else{'NO_FINDINGS'});problems=@($Problems);values_printed=$false;acceptance_record_changed=$false}
$Summary | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath ($Path+'.summary.json') -Encoding utf8
$Summary | ConvertTo-Json -Depth 4 -Compress | Write-Output
