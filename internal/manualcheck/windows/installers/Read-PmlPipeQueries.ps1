#Requires -Version 7.0
<#
Proof of the named-pipe information queries that Process Monitor prints as <Unknown>. The raw
backing log stores each such event as the IPC event class, operation QueryInformationFile, with the
information class and its length; Process Monitor 4.12 has no name for FilePipeInformation (23,
eight bytes) and FilePipeLocalInformation (24, forty bytes). A row is proven only when the record at
its ordinal agrees with the CSV row and the XML event in process, thread, time of day, path, and
result, names a pipe instance, and is one of those two queries with its documented length. Every
other record is listed as rejected with its reason, and a log outside the understood layout fails
the whole proof. The proof never changes a record; the reviewer decides what to do with it.
#>
[CmdletBinding()]
param([Parameter(Mandatory)][string]$Evidence,[Parameter(Mandatory)][string]$Csv,[Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Rows,[Parameter(Mandatory)][uint32[]]$Owners)
$ErrorActionPreference='Stop'
# Read-VerifiedArtifact resolves one immutable input and checks its recorded hash before parsing it.
function Read-VerifiedArtifact($Entry) {
    if(-not $Entry -or $Entry.sha256 -notmatch '^[a-fA-F0-9]{64}$' -or -not $Entry.path){throw 'Missing artifact/hash'}
    $Path=(Resolve-Path -LiteralPath $Entry.path).ProviderPath
    if((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -ine $Entry.sha256){throw 'Evidence artifact hash changed'}
    return $Path
}
$Basis='A record is accepted only when the raw log, the CSV row, and the XML event at the same ordinal agree in process, thread, time of day, path, and result, the path names a pipe instance, the result is SUCCESS, and the query is FilePipeInformation with length 8 or FilePipeLocalInformation with length 40. The reader understands 64-bit version 9 logs whose event count equals the export.'
$Result=[ordered]@{schema_version=1;complete=$false;failure='';pml_sha256='';xml_sha256='';csv_sha256='';records=0;accepted_full_ordinals=@();rejected=@();basis=$Basis}
try {
    $Bundle=Get-Content -Raw -LiteralPath $Evidence|ConvertFrom-Json
    if($Bundle.schema_version -ne 2){throw 'Kernel evidence schema must be 2'}
    $Pml=Read-VerifiedArtifact $Bundle.artifacts.pml;$Xml=Read-VerifiedArtifact $Bundle.artifacts.procmon_xml
    $CsvHash=(Get-FileHash -LiteralPath $Csv).Hash.ToLowerInvariant();$Export=Get-Content -Raw -LiteralPath ($Csv+'.export.json')|ConvertFrom-Json
    if($Export.exportAppliedDisplayFilter -isnot [bool] -or $Export.exportAppliedDisplayFilter -ne $false -or $Export.csvSha256 -ine $CsvHash -or $Export.sourcePmlSha256 -ine $Bundle.artifacts.pml.sha256 -or ($Export.PSObject.Properties.Name -contains 'sourceXmlSha256' -and $Export.sourceXmlSha256 -ine $Bundle.artifacts.procmon_xml.sha256)){throw 'Full export provenance does not bind this PML/CSV/XML'}
    $Result.pml_sha256=$Bundle.artifacts.pml.sha256.ToLowerInvariant();$Result.xml_sha256=$Bundle.artifacts.procmon_xml.sha256.ToLowerInvariant();$Result.csv_sha256=$CsvHash
    if(-not ('InstallerPmlPipeQueries' -as [type])){Add-Type -Path (Join-Path $PSScriptRoot 'PmlPipeQueries.cs')}
    $Records=[InstallerPmlPipeQueries]::Read($Pml,[long]$Rows.Count)
    $Result.records=$Records.Count
    $Seen=@{};$Candidates=@()
    foreach($R in $Records) {
        if($Seen.ContainsKey($R.Ordinal)){throw 'Duplicate raw-log ordinal'};$Seen[$R.Ordinal]=$true
        $Row=$Rows[$R.Ordinal-1];$EventPid=[uint32]0;$EventTid=[uint32]0;$Reason=''
        if([string]$Row.Operation -cne '<Unknown>'){$Reason='the CSV operation at this ordinal is not <Unknown>'}
        elseif(-not [uint32]::TryParse([string]$Row.PID,[ref]$EventPid) -or $EventPid -ne $R.Pid){$Reason='the process differs between the raw log and the CSV'}
        elseif($Owners -notcontains $EventPid){$Reason='the process is not attributed'}
        elseif(-not [uint32]::TryParse([string]$Row.TID,[ref]$EventTid) -or $EventTid -ne $R.Tid){$Reason='the thread differs between the raw log and the CSV'}
        elseif([string]$Row.Path -cne $R.Path){$Reason='the path differs between the raw log and the CSV'}
        elseif($R.Path -notmatch '^\\Device\\NamedPipe\\[^\\]'){$Reason='the path does not name a pipe instance'}
        elseif([string]$Row.Result -cne 'SUCCESS' -or $R.Status -ne 0){$Reason='the result is not SUCCESS in both the raw log and the CSV'}
        elseif(-not (($R.InformationClass -eq 23 -and $R.InformationLength -eq 8) -or ($R.InformationClass -eq 24 -and $R.InformationLength -eq 40))){$Reason='the query is not FilePipeInformation (23, 8 bytes) or FilePipeLocalInformation (24, 40 bytes)'}
        elseif($R.SubOperation -ne $R.InformationClass){$Reason='the sub-operation byte disagrees with the information class'}
        elseif($Row.PSObject.Properties.Name -notcontains 'Time of Day'){$Reason='the CSV has no Time of Day column'}
        else {
            $Ticks=[InstallerPmlPipeQueries]::TimeOfDayTicks([string]$Row.'Time of Day')
            if($Ticks -lt 0 -or [DateTime]::FromFileTime($R.FileTime).TimeOfDay.Ticks -ne $Ticks){$Reason='the time of day differs between the raw log and the CSV'}
        }
        if($Reason){$Result.rejected+=@{full_ordinal=$R.Ordinal;reason=$Reason};continue}
        $Candidates+=$R
    }
    if($Candidates.Count) {
        $XmlEvents=[InstallerPmlPipeQueries]::XmlEvents($Xml,[long[]]@($Candidates|ForEach-Object{$_.Ordinal}))
        foreach($R in $Candidates) {
            $Row=$Rows[$R.Ordinal-1];$E=$null;if($XmlEvents.ContainsKey([long]$R.Ordinal)){$E=$XmlEvents[[long]$R.Ordinal]}
            $Same=$null -ne $E
            foreach($Pair in @(@('Operation','Operation'),@('PID','PID'),@('TID','TID'),@('Path','Path'),@('Result','Result'),@('Time of Day','Time_of_Day'))) {
                if(-not $Same){break}
                if(-not $E.ContainsKey($Pair[1]) -or [string]$E[$Pair[1]] -cne [string]$Row.($Pair[0])){$Same=$false}
            }
            if($Same){$Result.accepted_full_ordinals+=$R.Ordinal}else{$Result.rejected+=@{full_ordinal=$R.Ordinal;reason='the XML event at this ordinal disagrees with the CSV row'}}
        }
    }
    $Result.complete=$true
} catch {
    $Result.complete=$false;$Result.accepted_full_ordinals=@();$Result.failure=$_.Exception.Message
}
[pscustomobject]$Result
