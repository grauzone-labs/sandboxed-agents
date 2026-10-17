# NuGet command package

This page tells you how to install, upgrade, and remove the `sandboxed-agents` command on Windows from the `SandboxedAgents` NuGet package. The package carries the Windows AMD64 executable, its `SHA256SUMS` manifest, and PowerShell 7 install and remove scripts under `tools/`.

## Where the package comes from

Every release carries `SandboxedAgents.X.Y.Z.nupkg` among its [release files](releases.md#release-files); the [releases page](releases.md#versions) says which kind of release the NuGet registry also holds. The package is a command package, not a library: nothing references it as a dependency.

## Install

You need Windows x64 and PowerShell 7 (`pwsh`). The scripts refuse to run on an older PowerShell.

1. Get the package. From a registry that hosts it, restore it with NuGet into an empty directory:

   ```powershell
   nuget install SandboxedAgents -Version 1.2.3 -OutputDirectory C:\Temp\sandboxed-agents-package
   ```

   The files land in the nested `SandboxedAgents.1.2.3\tools` directory. Point `$tools` at it:

   ```powershell
   $tools = 'C:\Temp\sandboxed-agents-package\SandboxedAgents.1.2.3\tools'
   ```
2. For a downloaded `.nupkg`, which is a zip archive, extract it to an empty directory and use its `tools` directory. If you extract it to `C:\Temp\sandboxed-agents-local`, the scripts are in:

   ```powershell
   $tools = 'C:\Temp\sandboxed-agents-local\tools'
   ```

   Unblock the downloaded scripts first. This needs no administrator rights; run it in PowerShell 7 before running the scripts:

   ```powershell
   Get-ChildItem -LiteralPath $tools -Filter *.ps1 | Unblock-File
   ```
3. Run the installer with PowerShell 7, using the `$tools` path from the step above:

   ```powershell
   pwsh -File "$tools\install-command.ps1"
   ```

The installer checks the executable against `SHA256SUMS` and stops on a mismatch. It then copies the executable to `sandboxed-agents.exe` in the install directory and checks the copy again. The default destination and the user `PATH` need no administrator rights.

- **Default destination.** `%LOCALAPPDATA%\Programs\sandboxed-agents`, which is per user. Pass `-InstallDirectory PATH` to choose another directory.
- **PATH.** The installer adds the install directory to the user `PATH` unless it is already listed. Open a new terminal afterwards to pick it up. Pass `-NoPathUpdate` to leave `PATH` alone. A directory whose path contains a semicolon requires `-NoPathUpdate`.

Check the result in a new terminal:

```powershell
sandboxed-agents version
```

## Upgrade

The package has no separate upgrade step. Get the newer package as above and run `pwsh -File "$tools\install-command.ps1"` again, with `$tools` pointing at the newer package. It replaces `sandboxed-agents.exe` in the install directory and leaves an existing `PATH` entry unchanged. If you installed with `-InstallDirectory`, pass the same directory again; otherwise the new version goes to the default destination and the old one stays where it was.

Close any running `sandboxed-agents.exe` process before rerunning the installer. Windows cannot replace an executable that is in use, so the installer fails if the old one is still running, and a failed replacement leaves the previous executable intact. Close the process and run the installer again.

## Remove

Run `remove-command.ps1` from the `tools` directory of any extracted version of the package, with `$tools` pointing at it:

```powershell
pwsh -File "$tools\remove-command.ps1"
```

It deletes `sandboxed-agents.exe` and deletes the install directory if that leaves it empty. Pass the same `-InstallDirectory` that you used to install, or the script looks in the default destination and leaves your custom installation in place.

- **PATH.** When the installer adds the install directory to the user `PATH`, it records that entry in a `.sandboxed-agents-path-state` file in the install directory. Removal deletes only the entry recorded as added by this installer, then the state file. If an equivalent entry was already on `PATH` before installation, the installer recorded nothing, and removal preserves that entry.
- **`-NoPathUpdate`.** Removal leaves `PATH` untouched and does not delete the state file. If the installer had added a `PATH` entry, the state file stays in the install directory, so that directory can remain even though `sandboxed-agents.exe` is gone.

Removing the command does not delete the extracted package directory. Delete that yourself.

The scripts change only the install directory and, unless `-NoPathUpdate` is given, the user `PATH`; what installing never touches is stated under [Installing](releases.md#installing).
