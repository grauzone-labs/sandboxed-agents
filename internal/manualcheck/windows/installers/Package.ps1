#Requires -Version 7.0
<#
Shared functions of the installer evidence package for item `installers-no-podman`. Every entry
script dot-sources this file and calls Import-InstallerConfiguration with the configuration that
`manual-check installers configure` wrote, which pins the candidate, the criterion revision, this
package, the frozen inputs, and the native recording tools before any capture.
#>
$ErrorActionPreference='Stop'
$script:Configuration=$null
# Write-NewJson creates evidence once using UTF8 and refuses to replace any existing artifact.
function Write-NewJson([string]$Path,$Value) {
 # -InputObject keeps an empty array as [] and a one-element array as an array; the pipeline form
 # emits nothing for an empty array, which made GetBytes throw on 2026-10-22.
 $Json=ConvertTo-Json -InputObject $Value -Depth 22
 if($null -eq $Json){throw "Write-NewJson: nothing to serialize for $Path"}
 $Bytes=[Text.UTF8Encoding]::new($false).GetBytes($Json)
 $Stream=[IO.File]::Open($Path,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::Read)
 try{$Stream.Write($Bytes,0,$Bytes.Length)}finally{$Stream.Dispose()}
}
# Import-InstallerConfiguration loads the pinned configuration and verifies this package's files
# and the native tools against it; the elevated roles also check the Microsoft signatures.
function Import-InstallerConfiguration([string]$Path,[switch]$Signatures) {
 if(-not $Path){throw 'The installer configuration path is required'}
 $Configuration=Get-Content -Raw -LiteralPath $Path|ConvertFrom-Json
 if($Configuration.schema_version -ne 1 -or $Configuration.candidate -notmatch '^[0-9a-f]{40}$' -or $Configuration.criterion_sha256 -notmatch '^[0-9a-f]{64}$'){throw 'Installer configuration identity missing'}
 if((Resolve-Path -LiteralPath $Configuration.package_directory).ProviderPath -ine (Resolve-Path -LiteralPath $PSScriptRoot).ProviderPath){throw 'The configuration pins another package directory'}
 $Pinned=@{}
 foreach($F in $Configuration.package_files){
  if($F.path -match '^[/\\]|(^|[/\\])\.\.([/\\]|$)'){throw 'Invalid package relative path'}
  $Full=Join-Path $PSScriptRoot $F.path
  if((Get-FileHash -LiteralPath $Full).Hash -ine $F.sha256){throw "Package file changed since configuration: $($F.path)"}
  $Pinned[(Resolve-Path -LiteralPath $Full).ProviderPath]=$true
 }
 foreach($Present in Get-ChildItem -LiteralPath $PSScriptRoot -File -Recurse|Where-Object{$_.Extension -in @('.ps1','.cs','.py','.wprp') -and $_.FullName -notmatch '[\\/]__pycache__[\\/]'}){
  if(-not $Pinned.ContainsKey($Present.FullName)){throw "Package file not pinned by the configuration: $($Present.Name)"}
 }
 foreach($Name in @('procmon','wpr','tracerpt')){
  $Tool=$Configuration.native_tools.$Name
  if(-not $Tool){throw "Native tool not configured: $Name"}
  if((Get-FileHash -LiteralPath $Tool.path).Hash -ine $Tool.sha256){throw "Tracing executable changed: $Name"}
  if($Signatures){
   # Procmon carries an embedded Microsoft signature. wpr.exe and tracerpt.exe are catalog-signed
   # Windows files, which Get-AuthenticodeSignature can report as NotSigned; they stay pinned by
   # hash and must live in the System32 directory of the captured host.
   $Sig=Get-AuthenticodeSignature -LiteralPath $Tool.path
   $Trusted=($Sig.Status -eq 'Valid' -and $Sig.SignerCertificate.Subject -match 'O=Microsoft Corporation')
   $System32=Join-Path ([Environment]::GetFolderPath('Windows')) 'System32'
   $InSystem32=((Split-Path -Parent (Resolve-Path -LiteralPath $Tool.path).ProviderPath) -ieq $System32)
   if($Name -eq 'procmon' -and -not $Trusted){throw "Elevated Microsoft signature verification failed: $Name"}
   if($Name -ne 'procmon' -and -not $Trusted -and -not ($Sig.Status -eq 'NotSigned' -and $InSystem32)){throw "Elevated Microsoft signature verification failed: $Name"}
  }
 }
 $script:Configuration=$Configuration
 $script:ConfigurationPath=(Resolve-Path -LiteralPath $Path).ProviderPath
 $script:ConfigurationSha256=(Get-FileHash -LiteralPath $Path).Hash.ToLowerInvariant()
 return $Configuration
}
# Read-Prepared loads a capture run's prepared.private.json and binds the configuration it names.
function Read-Prepared([string]$RunDirectory,[switch]$Signatures) {
 $Prepared=Get-Content -Raw -LiteralPath (Join-Path $RunDirectory 'prepared.private.json')|ConvertFrom-Json
 if($Prepared.schema_version -ne 3 -or $Prepared.method -notin @('binary','npm','nuget-root','nuget-default')){throw 'Prepared capture run is not of this package version'}
 if((Get-FileHash -LiteralPath $Prepared.configuration.path).Hash -ine $Prepared.configuration.sha256){throw 'Configuration changed since preparation'}
 [void](Import-InstallerConfiguration $Prepared.configuration.path -Signatures:$Signatures)
 if($script:Configuration.candidate -cne $Prepared.candidate -or $script:Configuration.criterion_sha256 -cne $Prepared.criterion_sha256){throw 'Prepared run and configuration disagree on candidate or criterion'}
 return $Prepared
}
# Assert-Role requires a Windows terminal with the intended elevation.
function Assert-Role([bool]$Elevated) {
 if(-not $IsWindows){throw 'Windows PowerShell7 required'}
 $Principal=[Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
 if($Principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) -ne $Elevated){throw 'Use the documented normal/elevated terminal role'}
}
# Invoke-RecordedNative records one native process and drains both outputs; timeouts preserve its exact identity.
function Invoke-RecordedNative([string]$Exe,[string[]]$Arguments,[string]$Root,[string]$Name,[int]$Seconds=300) {
 $Launch=Join-Path $Root ($Name+'.launch.json')
 if(Test-Path -LiteralPath $Launch){throw "Native action already attempted: $Name"}
 $Info=[Diagnostics.ProcessStartInfo]::new();$Info.FileName=$Exe;$Info.UseShellExecute=$false;$Info.RedirectStandardOutput=$true;$Info.RedirectStandardError=$true
 foreach($A in $Arguments){[void]$Info.ArgumentList.Add($A)}
 $P=[Diagnostics.Process]::Start($Info);$Start=$P.StartTime.ToUniversalTime()
 $Out=$P.StandardOutput.ReadToEndAsync();$Err=$P.StandardError.ReadToEndAsync()
 Write-NewJson $Launch @{pid=$P.Id;path=$Exe;start_ticks=$Start.Ticks;utc=$Start.ToString('o');arguments=$Arguments}
 if(-not $P.WaitForExit($Seconds*1000)){throw "Native action $Name still active PID=$($P.Id); preserve raw files; no duplicate or signal sent"}
 $Text=$Out.GetAwaiter().GetResult();$ErrorText=$Err.GetAwaiter().GetResult()
 [IO.File]::WriteAllText((Join-Path $Root ($Name+'.stdout.private.log')),$Text)
 [IO.File]::WriteAllText((Join-Path $Root ($Name+'.stderr.private.log')),$ErrorText)
 $End=$P.ExitTime.ToUniversalTime()
 Write-NewJson (Join-Path $Root ($Name+'.result.json')) @{exit=$P.ExitCode;finished_utc=$End.ToString('o')}
 return [pscustomobject]@{Exit=$P.ExitCode;Text=$Text;ErrorText=$ErrorText;StartUtc=$Start;EndUtc=$End}
}
# Save-Volume records the exact QueryDosDevice MULTI_SZ mapping and local fixed-NTFS properties.
function Save-Volume([string]$Path,[string]$Drive='C:') {
 if(-not ('InstallerVolume' -as [type])){Add-Type -TypeDefinition @'
using System;using System.Text;using System.Runtime.InteropServices;
public static class InstallerVolume {
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern uint QueryDosDevice(string name,char[] buffer,int length);
 public static string Read(string drive){var b=new char[32768];uint n=QueryDosDevice(drive,b,b.Length);if(n==0)throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());var names=new string(b,0,(int)n).Split(new[]{'\0'},StringSplitOptions.RemoveEmptyEntries);if(names.Length!=1)throw new Exception("Volume mapping is not unique");return names[0];}
}
'@}
 $D=[IO.DriveInfo]::new($Drive+'\');$Value=[ordered]@{utc=[DateTime]::UtcNow.ToString('o');drive=$Drive;nt_device=[InstallerVolume]::Read($Drive);filesystem=$D.DriveFormat;drive_type=[string]$D.DriveType;windows_directory=[Environment]::GetFolderPath('Windows')}
 if($Value.filesystem -cne 'NTFS' -or $Value.drive_type -cne 'Fixed'){throw 'Capture drive must be fixed NTFS'}
 Write-NewJson $Path $Value;return $Value
}
# Get-OwnedProcess proves PID, executable, start time and command before the caller signals or trusts it.
# The command line recorded by a PowerShell item shell comes from [Environment]::CommandLine, whose
# first token is the managed entry point pwsh.dll, while Win32_Process reports the native host
# pwsh.exe. Only that exact quoted first token, in the same directory as the recorded native path,
# is translated; every argument stays byte for byte, and the PID, path, start ticks, and CIM
# presence guards are unchanged.
function Get-OwnedProcess($Identity) {
 $P=Get-Process -Id ([int]$Identity.pid) -ErrorAction Stop
 $C=Get-CimInstance Win32_Process -Filter "ProcessId=$($Identity.pid)"
 $Expected=[string]$Identity.command_line
 $NativePath=[string]$Identity.path
 if($NativePath -match '(?i)(?:^|[\\/])pwsh\.exe$'){
  $ManagedToken='"'+$NativePath.Substring(0,$NativePath.Length-3)+'dll"'
  if($Expected.StartsWith($ManagedToken,[StringComparison]::Ordinal)){
   $Expected='"'+$NativePath+'"'+$Expected.Substring($ManagedToken.Length)
  }
 }
 if($P.Path -ine $Identity.path -or $P.StartTime.ToUniversalTime().Ticks -ne [long]$Identity.start_ticks -or -not $C -or ($Identity.command_line -and $C.CommandLine -cne $Expected)){throw 'Owned process identity changed; no signal sent'}
 return $P
}
