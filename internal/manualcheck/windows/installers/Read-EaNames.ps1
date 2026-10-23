#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][AllowEmptyString()][string]$Text,[Parameter(Mandatory)][int]$ExitCode)
# Only the known fsutil EA Name label is decoded. Unknown/localized formats return no proof.
if($ExitCode -ne 0){return}
$MatchesFound=[regex]::Matches($Text,'(?im)^\s*EA Name:\s*([^\r\n]+?)\s*$')
foreach($M in $MatchesFound){$M.Groups[1].Value.Trim()}
