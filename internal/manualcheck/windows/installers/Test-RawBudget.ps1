#Requires -Version 7.0
<#
Test-RawBudget decides whether the raw capture files may be exported. The Procmon backing file
is bounded by the 256 MiB raw limit the observer enforces during the capture. The kernel trace
is recorded into 128 MiB of memory buffers (InstallerJournal.wprp), and the saved ETL always
carries the rundown of processes, threads, and images on top of the buffers, so a complete
trace is larger than the buffers; 512 MiB bounds it with room for that rundown. Completeness is
decided by Read-KernelTraceEvidence (zero lost events and buffers, the Procmon lifetime present),
never by the size. The export's disk budget stays max(configured minimum, 50 x raw + 1 GiB).
#>
[CmdletBinding()]
param([Parameter(Mandatory)][long]$PmlBytes,[Parameter(Mandatory)][long]$EtlBytes)
$Reasons=@()
if($PmlBytes -le 0){$Reasons+='Procmon backing file is empty'}
if($PmlBytes -gt 256MB){$Reasons+='Procmon backing file exceeds 256 MiB'}
if($EtlBytes -le 0){$Reasons+='kernel trace is empty'}
if($EtlBytes -gt 512MB){$Reasons+='kernel trace exceeds 512 MiB'}
[pscustomobject]@{Ok=($Reasons.Count -eq 0);Reasons=$Reasons;RequiredFreeBytes=(($PmlBytes+$EtlBytes)*50+1GB)}
