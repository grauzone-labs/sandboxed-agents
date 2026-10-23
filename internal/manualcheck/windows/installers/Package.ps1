#Requires -Version 7.0
<#
Shared functions of the installer evidence package for item `installers-no-podman`. Every entry
script dot-sources this file and calls Import-InstallerConfiguration with the configuration that
`manual-check installers configure` wrote, which pins the candidate, the criterion revision, this
package, the frozen inputs, and the native recording tools before any capture.
#>
$ErrorActionPreference='Stop'
$script:Configuration=$null
# PackageExtensions are the kinds of package file that the configuration pins and a snapshot lists.
$script:PackageExtensions=@('.ps1','.cs','.py','.wprp')
# Write-NewBytes creates a file once with the given bytes and refuses to replace any existing file.
function Write-NewBytes([string]$Path,[byte[]]$Bytes,[IO.FileShare]$Share=[IO.FileShare]::Read) {
 $Stream=[IO.File]::Open($Path,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,$Share)
 try{$Stream.Write($Bytes,0,$Bytes.Length)}finally{$Stream.Dispose()}
}
# Write-NewJson creates evidence once using UTF8 and refuses to replace any existing artifact.
function Write-NewJson([string]$Path,$Value) {
 # -InputObject keeps an empty array as [] and a one-element array as an array; the pipeline form
 # emits nothing for an empty array, which made GetBytes throw on 2026-10-22.
 $Json=ConvertTo-Json -InputObject $Value -Depth 22
 if($null -eq $Json){throw "Write-NewJson: nothing to serialize for $Path"}
 Write-NewBytes $Path ([Text.UTF8Encoding]::new($false).GetBytes($Json))
}
# Write-NewText creates a text file once using UTF8 and refuses to replace any existing file.
function Write-NewText([string]$Path,[string]$Text) {
 Write-NewBytes $Path ([Text.UTF8Encoding]::new($false).GetBytes($Text))
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
  if(-not (Test-PackageRelativePath $F.path)){throw 'Invalid package relative path'}
  $Full=Join-Path $PSScriptRoot $F.path
  if((Get-FileHash -LiteralPath $Full).Hash -ine $F.sha256){throw "Package file changed since configuration: $($F.path)"}
  $Pinned[(Resolve-Path -LiteralPath $Full).ProviderPath]=$true
 }
 foreach($Present in Get-ChildItem -LiteralPath $PSScriptRoot -File -Recurse|Where-Object{$_.Extension -in $script:PackageExtensions -and $_.FullName -notmatch '[\\/]__pycache__[\\/]'}){
  if(-not $Pinned.ContainsKey($Present.FullName)){throw "Package file not pinned by the configuration: $($Present.Name)"}
 }
 foreach($Name in @('procmon','wpr','tracerpt')){
  $Tool=$Configuration.native_tools.$Name
  if(-not $Tool){throw "Native tool not configured: $Name"}
  if((Get-FileHash -LiteralPath $Tool.path).Hash -ine $Tool.sha256){throw "Tracing executable changed: $Name"}
  if($Signatures){
   # Procmon carries an embedded Microsoft signature. wpr.exe and tracerpt.exe are catalog-signed
   # Windows files, which Get-AuthenticodeSignature can report as NotSigned; they stay pinned by
   # hash and must be the System32 binaries of the captured host, as the snapshot also requires.
   $Trusted=Test-MicrosoftSignature $Tool.path
   if($Name -eq 'procmon' -and -not $Trusted){throw "Elevated Microsoft signature verification failed: $Name"}
   if($Name -ne 'procmon'){
    [void](Assert-System32Tool $Name ([string]$Tool.path))
    if(-not $Trusted -and (Get-AuthenticodeSignature -LiteralPath $Tool.path).Status -ne 'NotSigned'){throw "Elevated Microsoft signature verification failed: $Name"}
   }
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
# Set-ProtectedLocation makes an admin-only directory the current location of this PowerShell and of
# the process, so that neither an elevated native process nor a library this process loads by name
# can come from a directory another account can write.
function Set-ProtectedLocation([string]$Path){
 Set-Location -LiteralPath $Path
 [Environment]::CurrentDirectory=$Path
 $script:ProtectedLocation=$Path
}
# New-ProtectedStartInfo prepares the start of a native executable whose working directory is the
# protected location; it refuses to start anything before Set-ProtectedLocation chose one.
function New-ProtectedStartInfo([string]$Exe){
 if(-not $script:ProtectedLocation){throw 'Native tools start only from a protected working directory'}
 $Info=[Diagnostics.ProcessStartInfo]::new();$Info.FileName=$Exe;$Info.UseShellExecute=$false;$Info.WorkingDirectory=$script:ProtectedLocation
 return $Info
}
# Invoke-RecordedNative records one native process and drains both outputs; timeouts preserve its exact identity.
function Invoke-RecordedNative([string]$Exe,[string[]]$Arguments,[string]$Root,[string]$Name,[int]$Seconds=300) {
 $Launch=Join-Path $Root ($Name+'.launch.json')
 if(Test-Path -LiteralPath $Launch){throw "Native action already attempted: $Name"}
 $Info=New-ProtectedStartInfo $Exe;$Info.RedirectStandardOutput=$true;$Info.RedirectStandardError=$true
 foreach($A in $Arguments){[void]$Info.ArgumentList.Add($A)}
 $P=[Diagnostics.Process]::Start($Info);$Start=$P.StartTime.ToUniversalTime()
 $Out=$P.StandardOutput.ReadToEndAsync();$Err=$P.StandardError.ReadToEndAsync()
 Write-NewJson $Launch @{pid=$P.Id;path=$Exe;start_ticks=$Start.Ticks;utc=$Start.ToString('o');arguments=$Arguments}
 if(-not $P.WaitForExit($Seconds*1000)){throw "Native action $Name still active PID=$($P.Id); preserve raw files; no duplicate or signal sent"}
 $Text=$Out.GetAwaiter().GetResult();$ErrorText=$Err.GetAwaiter().GetResult()
 Write-NewText (Join-Path $Root ($Name+'.stdout.private.log')) $Text
 Write-NewText (Join-Path $Root ($Name+'.stderr.private.log')) $ErrorText
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

# ---- The trusted snapshot and the protected output directory ---------------------------------------
# The elevated observer is started by the person before any installer runs, so the package it starts
# from is the trust anchor. Before CAPTURE_READY it copies every pinned package file, the configuration,
# and Procmon into a fresh directory that only Administrators and SYSTEM can change, creates the run's
# protected output directory, and binds the run's records and directories in the snapshot manifest.
# After the workload, everything elevated runs from the snapshot, writes only into the output
# directory, and reads what the item shell wrote only through Read-BoundInput.

# TrustedSids are the principals allowed to change the snapshot: Administrators, SYSTEM, and
# TrustedInstaller.
$script:TrustedSids=@('S-1-5-32-544','S-1-5-18','S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464')
# ChangeRights let an account change a file or directory: write data or create files (0x2), append data
# or create folders (0x4), write extended attributes (0x10), delete child (0x40), write attributes
# (0x100), delete (0x10000), change permissions (0x40000), take ownership (0x80000), generic all
# (0x10000000), and generic write (0x40000000).
$script:ChangeRights=0x2 -bor 0x4 -bor 0x10 -bor 0x40 -bor 0x100 -bor 0x10000 -bor 0x40000 -bor 0x80000 -bor 0x10000000 -bor 0x40000000
# ReplaceRights on an ancestor let an account move, delete, or re-permission what lies below it: delete
# child (0x40), delete (0x10000), change permissions (0x40000), take ownership (0x80000), generic all
# (0x10000000).
$script:ReplaceRights=0x40 -bor 0x10000 -bor 0x40000 -bor 0x80000 -bor 0x10000000

# Get-TrustedBase returns the directory below which snapshots and output directories live,
# sandboxed-agents-manual in the machine's program data folder, read through the known-folder API and
# not from an environment variable.
function Get-TrustedBase{Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::CommonApplicationData)) 'sandboxed-agents-manual'}

# Get-OutputDirectory returns the protected output directory of a run below the trusted base; the item
# shell and the runner find the observer's records there.
function Get-OutputDirectory([string]$RunId){Join-Path (Join-Path (Get-TrustedBase) 'output') $RunId}

# Get-ObserverSid returns the SID of the account this process runs under.
function Get-ObserverSid{[Security.Principal.WindowsIdentity]::GetCurrent().User.Value}

# Get-ProcessOwnerSid returns the SID of the account a process runs under, as Windows reports it.
function Get-ProcessOwnerSid([int]$ProcessId){
 $Owner=Get-CimInstance Win32_Process -Filter "ProcessId=$ProcessId"|Invoke-CimMethod -MethodName GetOwnerSid
 if($Owner.ReturnValue -ne 0 -or -not $Owner.Sid){throw "The account of process $ProcessId cannot be read"}
 return [string]$Owner.Sid
}

# Test-AdministratorAccount tells whether this process's account is an administrator: its token
# carries the Administrators SID, enabled or deny-only, as under a UAC split token.
function Test-AdministratorAccount{[bool]@([Security.Principal.WindowsIdentity]::GetCurrent().Claims|Where-Object{$_.Value -eq 'S-1-5-32-544'}).Count}

# Assert-SeparateObserver refuses an observer that shares the item shell's account, because the
# elevated PowerShell and Procmon load code and settings from their account's profile, which the
# workload could then change. The item shell's SID comes from its process and must be the SID its
# context recorded, and that account must be a standard user. It returns the item shell's SID.
function Assert-SeparateObserver($Item){
 if($Item.administrator_account -ne $false){throw 'The item shell account must be a standard user, not an administrator; start the item from a standard account'}
 $ItemSid=Get-ProcessOwnerSid ([int]$Item.pid)
 if($ItemSid -ine [string]$Item.user_sid){throw 'The item shell process runs under another account than its context names'}
 if((Get-ObserverSid) -ieq $ItemSid){throw "The observer runs under the item shell's account; open the elevated terminal with a separate administrator account"}
 return $ItemSid
}

# Test-PackageRelativePath tells whether a path is relative, stays inside its root, and names no drive
# or stream.
function Test-PackageRelativePath([string]$Path){return ($Path -and $Path -notmatch '^[/\\]|(^|[/\\])\.\.([/\\]|$)|:')}

# Test-MicrosoftSignature tells whether a file carries a valid Authenticode signature of Microsoft.
function Test-MicrosoftSignature([string]$Path){
 $Signature=Get-AuthenticodeSignature -LiteralPath $Path
 return ($Signature.Status -eq 'Valid' -and $Signature.SignerCertificate.Subject -match 'O=Microsoft Corporation')
}

# Get-SystemDirectory returns the System32 directory of the running Windows, through the known-folder API.
function Get-SystemDirectory{[Environment]::GetFolderPath([Environment+SpecialFolder]::System)}

# Assert-System32Tool refuses a native tool path other than NAME.exe in System32, which no other
# account can replace, and returns that path.
function Assert-System32Tool([string]$Name,[string]$Path){
 $Expected=Join-Path (Get-SystemDirectory) ($Name+'.exe')
 if(-not $Path -or [IO.Path]::GetFullPath($Path) -ine [IO.Path]::GetFullPath($Expected)){throw "The configuration must pin $Name from System32 ($Expected), which no other account can replace"}
 return $Expected
}

# Get-PathSecurity reads a path's security as the checks need it: whether it is a reparse point, its
# owner SID, and its access rules with SID, kind, rights, and whether a rule applies only to children.
# A reparse point is reported as such and never followed.
function Get-PathSecurity([string]$Path){
 $Item=Get-Item -LiteralPath $Path -Force
 if($Item.Attributes -band [IO.FileAttributes]::ReparsePoint){return [pscustomobject]@{ReparsePoint=$true;Owner='';Rules=@()}}
 $Acl=if($Item.PSIsContainer){[IO.FileSystemAclExtensions]::GetAccessControl([IO.DirectoryInfo]::new($Item.FullName))}else{[IO.FileSystemAclExtensions]::GetAccessControl([IO.FileInfo]::new($Item.FullName))}
 $Rules=foreach($R in $Acl.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier])){
  [pscustomobject]@{Sid=$R.IdentityReference.Value;Allow=($R.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow);Rights=[int]$R.FileSystemRights;InheritOnly=[bool]($R.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly)}
 }
 [pscustomobject]@{ReparsePoint=$false;Owner=$Acl.GetOwner([Security.Principal.SecurityIdentifier]).Value;Rules=@($Rules)}
}

# Test-AdminOnlyPath tells whether only Administrators, SYSTEM, or TrustedInstaller can change a path:
# it is no reparse point, one of them owns it, and no allow rule, including one that only children
# inherit, gives anyone else a change right. A read grant does not make a path changeable.
function Test-AdminOnlyPath([string]$Path){
 $Security=Get-PathSecurity $Path
 if($Security.ReparsePoint -or $script:TrustedSids -notcontains $Security.Owner){return $false}
 foreach($R in $Security.Rules){if($R.Allow -and ($R.Rights -band $script:ChangeRights) -and $script:TrustedSids -notcontains $R.Sid){return $false}}
 return $true
}

# Assert-AncestorsCannotReplace refuses a path any of whose ancestors, up to the volume root, is a
# reparse point, is owned by another account, or lets another account delete, rename, or re-permission
# what lies below it; creating new entries in an ancestor is no threat to an existing child.
function Assert-AncestorsCannotReplace([string]$Path){
 $Parent=[IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($Path))
 while($Parent){
  $Security=Get-PathSecurity $Parent
  if($Security.ReparsePoint){throw "Trusted directory ancestor $Parent is a reparse point"}
  if($script:TrustedSids -notcontains $Security.Owner){throw "Trusted directory ancestor $Parent is owned by another account, which could replace what lies below it"}
  foreach($R in $Security.Rules){if($R.Allow -and -not $R.InheritOnly -and ($R.Rights -band $script:ReplaceRights) -and $script:TrustedSids -notcontains $R.Sid){throw "Trusted directory ancestor $Parent lets another account replace what lies below it"}}
  $Next=[IO.Path]::GetDirectoryName($Parent)
  if($Next -eq $Parent){break}
  $Parent=$Next
 }
}

# New-AdminOnlySecurity builds the protected security descriptor of the snapshot and the output
# directory: Administrators own it, Administrators and SYSTEM have full control, nothing is inherited
# from above, and nobody else has any right, except that a reader SID, when given, may read.
function New-AdminOnlySecurity([bool]$Directory,[string]$ReaderSid=''){
 $Admins=[Security.Principal.SecurityIdentifier]::new('S-1-5-32-544');$System=[Security.Principal.SecurityIdentifier]::new('S-1-5-18')
 $Security=if($Directory){[Security.AccessControl.DirectorySecurity]::new()}else{[Security.AccessControl.FileSecurity]::new()}
 $Security.SetOwner($Admins);$Security.SetAccessRuleProtection($true,$false)
 $Inherit=if($Directory){[Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'}else{[Security.AccessControl.InheritanceFlags]::None}
 foreach($Sid in @($Admins,$System)){$Security.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($Sid,[Security.AccessControl.FileSystemRights]::FullControl,$Inherit,[Security.AccessControl.PropagationFlags]::None,[Security.AccessControl.AccessControlType]::Allow))}
 if($ReaderSid){$Security.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($ReaderSid),[Security.AccessControl.FileSystemRights]::ReadAndExecute,$Inherit,[Security.AccessControl.PropagationFlags]::None,[Security.AccessControl.AccessControlType]::Allow))}
 return $Security
}

# New-AdminOnlyDirectory creates a directory with the protected security descriptor applied at
# creation, optionally readable by one SID, refuses a path that already exists, and checks the result,
# so that a directory another account created in between is refused rather than used.
function New-AdminOnlyDirectory([string]$Path,[string]$ReaderSid=''){
 if(Test-Path -LiteralPath $Path){throw "Trusted directory already exists: $Path"}
 [void][IO.FileSystemAclExtensions]::Create([IO.DirectoryInfo]::new($Path),(New-AdminOnlySecurity $true $ReaderSid))
 if(-not (Test-AdminOnlyPath $Path)){throw "Trusted directory $Path is not admin-only after creation"}
}

# Protect-TrustedFile applies the protected security descriptor to a file of the snapshot and checks it.
function Protect-TrustedFile([string]$Path){
 [IO.FileSystemAclExtensions]::SetAccessControl([IO.FileInfo]::new($Path),(New-AdminOnlySecurity $false))
 if(-not (Test-AdminOnlyPath $Path)){throw "Trusted file $Path is not admin-only after protection"}
}

# Initialize-TrustedDirectory creates an admin-only directory, or accepts an existing one only when it
# is admin-only and no ancestor lets another account replace it; anything else is refused, never
# repaired.
function Initialize-TrustedDirectory([string]$Path){
 Assert-AncestorsCannotReplace $Path
 if(Test-Path -LiteralPath $Path){
  if(-not (Test-AdminOnlyPath $Path)){throw "Trusted directory $Path exists and is not admin-only; remove it in an elevated terminal and start again"}
  return
 }
 New-AdminOnlyDirectory $Path
}

# Copy-PinnedFile reads a source file once, refuses it unless those bytes have the pinned SHA-256,
# writes the same bytes to a new file of the snapshot, protects it, and checks the written file again.
function Copy-PinnedFile([string]$Source,[string]$Destination,[string]$Sha256,[string]$Name){
 $Bytes=[IO.File]::ReadAllBytes($Source)
 if([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)) -ine $Sha256){throw "Pinned file changed before the snapshot: $Name"}
 Write-NewBytes $Destination $Bytes ([IO.FileShare]::None)
 Protect-TrustedFile $Destination
 if((Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash -ine $Sha256){throw "Snapshot copy differs from its pin: $Name"}
}

# Add-InstallerHandleType loads the Windows calls that report the final path and the link count of an
# open handle and the final path of a directory. The directory is opened with FILE_FLAG_BACKUP_SEMANTICS
# (0x02000000) and SECURITY_SQOS_PRESENT (0x00100000) with SECURITY_IDENTIFICATION (0x00010000), so a
# pipe server in place of the path could identify this process but never impersonate it. The files
# Read-BoundInput opens go through .NET's FileStream, which sets SECURITY_SQOS_PRESENT with
# SECURITY_ANONYMOUS itself.
function Add-InstallerHandleType{
 if('InstallerHandle' -as [type]){return}
 Add-Type -TypeDefinition @'
using System;using System.Text;using System.Runtime.InteropServices;using Microsoft.Win32.SafeHandles;
public static class InstallerHandle {
 [StructLayout(LayoutKind.Sequential,Pack=4)] struct Information {public uint Attributes;public long Created;public long Accessed;public long Written;public uint Volume;public uint SizeHigh;public uint SizeLow;public uint Links;public uint IndexHigh;public uint IndexLow;}
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetFileInformationByHandle(SafeFileHandle handle,out Information information);
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern uint GetFinalPathNameByHandleW(SafeFileHandle handle,StringBuilder buffer,uint length,uint flags);
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern SafeFileHandle CreateFileW(string name,uint access,uint share,IntPtr security,uint disposition,uint flags,IntPtr template);
 static Information Read(SafeFileHandle handle){Information i;if(!GetFileInformationByHandle(handle,out i))throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());return i;}
 public static string FinalPath(SafeFileHandle handle){var b=new StringBuilder(32768);uint n=GetFinalPathNameByHandleW(handle,b,(uint)b.Capacity,0);if(n==0||n>=b.Capacity)throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());var p=b.ToString();if(p.StartsWith(@"\\?\UNC\"))return @"\\"+p.Substring(8);if(p.StartsWith(@"\\?\"))return p.Substring(4);return p;}
 public static uint Links(SafeFileHandle handle){return Read(handle).Links;}
 public static string FinalDirectory(string path){using(var h=CreateFileW(path,0x80,7,IntPtr.Zero,3,0x02000000|0x00100000|0x00010000,IntPtr.Zero)){if(h.IsInvalid)throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());if((Read(h).Attributes&0x10)==0)throw new InvalidOperationException("Not a directory: "+path);return FinalPath(h);}}
}
'@
}

# Get-FinalDirectory returns the final path of an existing directory with every link resolved. On
# Windows it asks the open directory handle. Elsewhere, where only the offline tests run this package,
# it asks realpath.
function Get-FinalDirectory([string]$Path){
 if($IsWindows){Add-InstallerHandleType;return [InstallerHandle]::FinalDirectory($Path)}
 $Final=& realpath -e -- $Path;if($LASTEXITCODE -ne 0 -or -not $Final){throw "Directory not found: $Path"};return [string]$Final
}

# Get-StreamIdentity returns the final path and the link count of an open file, taken from its handle
# and not from a path that could change in between. Elsewhere than Windows, where only the offline
# tests run this package, /proc names the open descriptor.
function Get-StreamIdentity([IO.FileStream]$Stream){
 if($IsWindows){Add-InstallerHandleType;return [pscustomobject]@{Path=[InstallerHandle]::FinalPath($Stream.SafeFileHandle);Links=[int][InstallerHandle]::Links($Stream.SafeFileHandle)}}
 $Descriptor=$Stream.SafeFileHandle.DangerousGetHandle().ToInt32()
 $Path=[IO.File]::ResolveLinkTarget("/proc/self/fd/$Descriptor",$false).FullName
 $Links=& stat -L -c '%h' -- "/proc/$PID/fd/$Descriptor";if($LASTEXITCODE -ne 0){throw 'The link count of an open input cannot be read'}
 return [pscustomobject]@{Path=$Path;Links=[int]$Links}
}

# Test-SharingViolation tells whether an error only says that another handle still holds the file:
# ERROR_SHARING_VIOLATION or ERROR_LOCK_VIOLATION on Windows, the lock conflict elsewhere, where only
# the offline tests run this package. Access, missing-file, and every other error are not.
function Test-SharingViolation($Exception){
 while($Exception -is [Management.Automation.MethodInvocationException] -and $Exception.InnerException){$Exception=$Exception.InnerException}
 if($Exception -isnot [IO.IOException] -or $Exception -is [IO.FileNotFoundException] -or $Exception -is [IO.DirectoryNotFoundException]){return $false}
 if($IsWindows){return ($Exception.HResult -eq [int]0x80070020 -or $Exception.HResult -eq [int]0x80070021)}
 return ($Exception.HResult -eq 11)
}

# Read-BoundInput reads a file that the item shell wrote, by its fixed name in a directory the snapshot
# bound before the workload. It returns $null when the file does not exist, and, with
# -PendingWhileWritten, also while the item shell still holds the file open for writing; the observer's
# waits use that inside their time budgets, and every other error still stops the caller. It returns
# the bytes only when the open handle names exactly that file and the file has a single name, so no
# link or replaced directory can make the elevated reader read another file.
function Read-BoundInput([string]$Directory,[string]$Name,[long]$MaxBytes=256MB,[switch]$PendingWhileWritten){
 if(-not (Test-PackageRelativePath $Name) -or $Name -match '[/\\]'){throw "Invalid input name: $Name"}
 $Path=Join-Path $Directory $Name
 try{$Stream=[IO.File]::Open($Path,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)}
 catch [IO.FileNotFoundException],[IO.DirectoryNotFoundException]{return $null}
 catch{if($PendingWhileWritten -and (Test-SharingViolation $_.Exception)){return $null};throw}
 try{
  $Identity=Get-StreamIdentity $Stream
  if($Identity.Path -ine $Path){throw "Input $Name is not the file $Path; nothing was read from it"}
  if($Identity.Links -ne 1){throw "Input $Name has more than one name; nothing was read from it"}
  if($Stream.Length -gt $MaxBytes){throw "Input $Name exceeds $MaxBytes bytes"}
  $Bytes=[byte[]]::new($Stream.Length);$Read=0
  while($Read -lt $Bytes.Length){$N=$Stream.Read($Bytes,$Read,$Bytes.Length-$Read);if($N -le 0){throw "Input $Name ended early"};$Read+=$N}
 } finally {$Stream.Dispose()}
 return ,$Bytes
}

# New-TrustedSnapshot runs in the elevated observer before CAPTURE_READY, after Read-Prepared imported
# and verified the configuration and Assert-SeparateObserver returned the item shell's SID. It copies
# every pinned package file, the configuration, and Procmon into a fresh admin-only directory for the
# run; refuses any byte that differs from its pin and any wpr or tracerpt other than the System32
# binary; creates the run's protected output directory, readable by the item shell's account; binds
# the prepared record, the item context, and the final paths of the directories the item shell writes
# into, in the manifest; and returns the snapshot as Read-TrustedSnapshot reads it back.
function New-TrustedSnapshot($Prepared,$Item,[string]$ItemSid,[string]$RunDirectory,[string]$PackageDirectory){
 if(-not $script:Configuration -or -not $script:ConfigurationPath){throw 'Import the configuration before taking the snapshot'}
 $RunId=[string]$Prepared.run_id
 if($RunId -notmatch '^[A-Za-z0-9][A-Za-z0-9.-]*$'){throw 'The run ID cannot name a trusted directory'}
 if($ItemSid -notmatch '^S-1-[0-9-]+$'){throw 'The item shell SID is missing'}
 $Logs=Join-Path $Prepared.install_root 'logs'
 if([string]$Item.completion_file -ine (Join-Path $Logs 'method-complete.json')){throw 'The item context names another completion file'}
 $Bound=[ordered]@{run=(Get-FinalDirectory $RunDirectory);fake_podman=(Get-FinalDirectory (Join-Path $RunDirectory 'fake-podman'));install_logs=(Get-FinalDirectory $Logs)}
 $Base=Get-TrustedBase;Initialize-TrustedDirectory $Base
 $Parent=Join-Path $Base 'trusted';Initialize-TrustedDirectory $Parent
 $OutputParent=Join-Path $Base 'output';Initialize-TrustedDirectory $OutputParent
 $Root=Join-Path $Parent $RunId;$Output=Join-Path $OutputParent $RunId
 Assert-AncestorsCannotReplace $Root;Assert-AncestorsCannotReplace $Output
 New-AdminOnlyDirectory $Root
 New-AdminOnlyDirectory $Output $ItemSid
 $Files=@()
 foreach($F in $script:Configuration.package_files){
  if(-not (Test-PackageRelativePath $F.path)){throw "Invalid package relative path: $($F.path)"}
  $Destination=Join-Path $Root $F.path
  $Missing=@();$Directory=Split-Path -Parent $Destination
  while(-not (Test-Path -LiteralPath $Directory)){$Missing=@($Directory)+$Missing;$Directory=Split-Path -Parent $Directory}
  foreach($D in $Missing){New-AdminOnlyDirectory $D}
  Copy-PinnedFile (Join-Path $PackageDirectory $F.path) $Destination $F.sha256 $F.path
  $Files+=@{path=$F.path;sha256=$F.sha256.ToLowerInvariant()}
 }
 Copy-PinnedFile $script:ConfigurationPath (Join-Path $Root 'configuration.json') $script:ConfigurationSha256 'configuration'
 $Native=Join-Path $Root 'native';New-AdminOnlyDirectory $Native
 $Procmon=Join-Path $Native 'Procmon64.exe'
 Copy-PinnedFile $script:Configuration.native_tools.procmon.path $Procmon $script:Configuration.native_tools.procmon.sha256 'Procmon'
 if(-not (Test-MicrosoftSignature $Procmon)){throw 'The snapshot Procmon lacks a valid Microsoft signature'}
 $Tools=@{}
 foreach($Name in @('wpr','tracerpt')){$Tool=$script:Configuration.native_tools.$Name;$Tools[$Name]=@{path=(Assert-System32Tool $Name ([string]$Tool.path));sha256=([string]$Tool.sha256).ToLowerInvariant()}}
 $Manifest=Join-Path $Root 'trusted-snapshot.json'
 Write-NewJson $Manifest @{schema_version=2;run_id=$RunId;created_utc=[DateTime]::UtcNow.ToString('o');configuration_sha256=$script:ConfigurationSha256;files=$Files;procmon=@{path='native/Procmon64.exe';sha256=([string]$script:Configuration.native_tools.procmon.sha256).ToLowerInvariant()};wpr=$Tools.wpr;tracerpt=$Tools.tracerpt;output_directory=$Output;item_sid=$ItemSid;bound_directories=$Bound;prepared=$Prepared;item=$Item}
 Protect-TrustedFile $Manifest
 return Read-TrustedSnapshot $Root
}

# Read-TrustedSnapshot accepts a snapshot directory only when it lies at trusted\RUN below the trusted
# base; the base, trusted, the snapshot, every directory in it, its manifest, every file the manifest
# lists, and the run's output directory are admin-only; no ancestor lets another account replace them;
# every listed file has its pinned SHA-256; no unlisted package file is present; the files are exactly
# the configuration's package pins; Procmon is signed by Microsoft; and wpr and tracerpt are the pinned
# System32 binaries. It makes the snapshot's configuration the imported one ($script:Configuration and
# its path and hash) and returns the snapshot's paths, configuration, output directory, and bound run
# state; nothing it returns comes from the run directory.
function Read-TrustedSnapshot([string]$Root){
 $Root=[IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar,[IO.Path]::AltDirectorySeparatorChar)
 $Parent=Split-Path -Parent $Root;$Base=Split-Path -Parent $Parent
 if((Split-Path -Leaf $Parent) -cne 'trusted' -or $Base -ine [IO.Path]::GetFullPath((Get-TrustedBase))){throw "Not a trusted snapshot directory: $Root; run the elevated steps from the snapshot the observer printed"}
 Assert-AncestorsCannotReplace $Base
 foreach($D in @($Base,$Parent,$Root)){if(-not (Test-AdminOnlyPath $D)){throw "Trusted snapshot directory $D is not admin-only"}}
 foreach($D in Get-ChildItem -LiteralPath $Root -Directory -Recurse -Force){if(-not (Test-AdminOnlyPath $D.FullName)){throw "Trusted snapshot directory $($D.FullName) is not admin-only"}}
 $ManifestPath=Join-Path $Root 'trusted-snapshot.json'
 if(-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf) -or -not (Test-AdminOnlyPath $ManifestPath)){throw "Trusted snapshot manifest missing or not admin-only in $Root"}
 $Manifest=Get-Content -Raw -LiteralPath $ManifestPath|ConvertFrom-Json
 $RunId=Split-Path -Leaf $Root
 if($Manifest.schema_version -ne 2 -or [string]$Manifest.run_id -cne $RunId -or [string]$Manifest.prepared.run_id -cne $RunId){throw "Trusted snapshot manifest does not describe $Root"}
 $Output=[string]$Manifest.output_directory
 if($Output -ine (Get-OutputDirectory $RunId)){throw 'Trusted snapshot names another output directory'}
 Assert-AncestorsCannotReplace $Output
 if(-not (Test-AdminOnlyPath (Split-Path -Parent $Output)) -or -not (Test-AdminOnlyPath $Output)){throw "Output directory $Output is not admin-only"}
 $Listed=[Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
 $Verify={param([string]$Relative,[string]$Sha256)
  if(-not (Test-PackageRelativePath $Relative)){throw "Invalid trusted snapshot path: $Relative"}
  $File=[IO.Path]::GetFullPath((Join-Path $Root $Relative))
  if(-not (Test-Path -LiteralPath $File -PathType Leaf) -or -not (Test-AdminOnlyPath $File)){throw "Trusted snapshot file missing or not admin-only: $Relative"}
  if((Get-FileHash -LiteralPath $File -Algorithm SHA256).Hash -ine $Sha256){throw "Trusted snapshot file changed: $Relative"}
  [void]$Listed.Add($File);$File}
 foreach($F in $Manifest.files){[void](& $Verify $F.path $F.sha256)}
 $ConfigurationPath=& $Verify 'configuration.json' $Manifest.configuration_sha256
 $Procmon=& $Verify $Manifest.procmon.path $Manifest.procmon.sha256
 foreach($F in Get-ChildItem -LiteralPath $Root -File -Recurse -Force){if($F.Extension -in $script:PackageExtensions -and -not $Listed.Contains($F.FullName)){throw "Unlisted file in the trusted snapshot: $($F.Name)"}}
 $Configuration=Get-Content -Raw -LiteralPath $ConfigurationPath|ConvertFrom-Json
 $Pins=@($Configuration.package_files|ForEach-Object{$_.path+'='+([string]$_.sha256).ToLowerInvariant()}|Sort-Object)
 $Copied=@($Manifest.files|ForEach-Object{$_.path+'='+([string]$_.sha256).ToLowerInvariant()}|Sort-Object)
 if(($Pins -join '|') -cne ($Copied -join '|')){throw 'Trusted snapshot files differ from the configuration pins'}
 if($Manifest.procmon.sha256 -ine $Configuration.native_tools.procmon.sha256){throw 'Trusted snapshot Procmon differs from the configuration pin'}
 if(-not (Test-MicrosoftSignature $Procmon)){throw 'Trusted snapshot Procmon lacks a valid Microsoft signature'}
 $Tools=@{}
 foreach($Name in @('wpr','tracerpt')){
  $Entry=$Manifest.$Name;$Expected=Assert-System32Tool $Name ([string]$Entry.path)
  if($Entry.sha256 -ine $Configuration.native_tools.$Name.sha256){throw "Trusted snapshot $Name is not the pinned System32 binary"}
  if((Get-FileHash -LiteralPath $Expected -Algorithm SHA256).Hash -ine $Entry.sha256){throw "System32 $Name differs from its pin"}
  $Tools[$Name]=$Expected
 }
 $Prepared=$Manifest.prepared
 if([string]$Prepared.candidate -cne $Configuration.candidate -or [string]$Prepared.criterion_sha256 -cne $Configuration.criterion_sha256 -or [string]$Prepared.configuration.sha256 -ine $Manifest.configuration_sha256){throw 'The bound prepared record names another candidate, criterion, or configuration than the snapshot'}
 $script:Configuration=$Configuration;$script:ConfigurationPath=$ConfigurationPath;$script:ConfigurationSha256=([string]$Manifest.configuration_sha256).ToLowerInvariant()
 [pscustomobject]@{Root=$Root;RunId=$RunId;Configuration=$Configuration;ConfigurationSha256=$script:ConfigurationSha256
  Procmon=$Procmon;ProcmonSha256=([string]$Manifest.procmon.sha256).ToLowerInvariant();Wpr=$Tools.wpr;WprSha256=([string]$Manifest.wpr.sha256).ToLowerInvariant();Tracerpt=$Tools.tracerpt;TracerptSha256=([string]$Manifest.tracerpt.sha256).ToLowerInvariant()
  OutputDirectory=$Output;ItemSid=[string]$Manifest.item_sid;Bound=$Manifest.bound_directories;Prepared=$Prepared;Item=$Manifest.item}
}
