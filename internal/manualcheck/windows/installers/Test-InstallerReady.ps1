#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)]$Record,[Parameter(Mandatory)]$Context)
$ErrorActionPreference='Stop'
foreach($Flag in @($Record.elevated_observer,$Record.wpr.active,$Record.procmon.advanced_output,$Record.procmon.drop_filtered_events)){if($Flag -isnot [bool]){throw 'Readiness booleans must be actual booleans'}}
if($Record.schema_version -ne 2 -or $Record.nonce -cne $Context.nonce -or $Record.method -cne $Context.method -or $Record.item_pid -ne $Context.pid -or $Record.elevated_observer -ne $true){throw 'Observer/item identity mismatch'}
if($Record.wpr.active -ne $true -or $Record.wpr.start_exit -ne 0 -or $Record.wpr.collectors_exit -ne 0 -or $Record.wpr.profiles_exit -ne 0 -or @($Record.wpr.stacks) -cnotcontains 'FileWrite'){throw 'WPR file-write stacks not ready'}
if($Record.procmon.pid -le 0 -or $Record.procmon.start_ticks -le 0 -or $Record.procmon.advanced_output -ne $false -or $Record.procmon.drop_filtered_events -ne $false -or $Record.procmon.confirmed_by -cne 'human-elevated-observer'){throw 'Procmon controls not ready'}
foreach($Activity in @('File System','Registry','Process/Thread')){if(@($Record.procmon.activity) -cnotcontains $Activity){throw 'Procmon activity missing'}}
foreach($Column in @('Date & Time','Thread ID','Duration')){if(@($Record.procmon.columns) -cnotcontains $Column){throw 'Procmon columns missing'}}
$true
