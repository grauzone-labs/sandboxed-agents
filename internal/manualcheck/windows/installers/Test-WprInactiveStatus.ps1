#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][int]$ExitCode,[Parameter(Mandatory)][AllowEmptyString()][string]$Stdout,[AllowEmptyString()][string]$Stderr='')
# Test-WprInactiveStatus accepts the observed successful no-recording report only; empty, active, error, and unknown output remain unproven.
if($ExitCode -ne 0 -or -not [string]::IsNullOrWhiteSpace($Stderr)){return $false}
$Lines=@($Stdout -split '\r?\n'|ForEach-Object{$_.Trim()}|Where-Object{$_})
$Absence=@($Lines|Where-Object{$_ -ceq 'WPR is not recording'})
if($Absence.Count -ne 1){return $false}
foreach($Line in $Lines){
 if($Line -ceq 'WPR is not recording'){continue}
 if($Line -cmatch '^Microsoft Windows Performance Recorder Version \d+(?:\.\d+){1,3} \(CoreSystem\)$'){continue}
 if($Line -cmatch '^Copyright \(c\) \d{4} Microsoft Corporation\. All rights reserved\.$'){continue}
 return $false
}
$true
