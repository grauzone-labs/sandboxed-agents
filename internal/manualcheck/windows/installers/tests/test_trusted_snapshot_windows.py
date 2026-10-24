"""Exercise the trusted snapshot, the protected output directory, the account check, and the bound-input
reader of Package.ps1 against real Windows security descriptors, links, handles, and accounts.

Nothing is substituted except the trusted base, which is a fresh directory below the real program data
folder, so the real C:\\ and C:\\ProgramData descriptors are the ancestors the checks read. The tests need
Windows and an elevated process; elsewhere they are skipped, and with
SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1 a missing precondition fails instead of skipping. They prove the
behaviour on the machine that runs them, not on a capture host.
"""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
import uuid
from pathlib import Path

from test_trusted_snapshot import RUN_ID, Workspace, digest, pwsh

STRICT = os.environ.get('SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT') == '1'
USERS = 'S-1-5-32-545'
# ChangeRights is the mask of Package.ps1's change rights; a read grant carries none of them.
CHANGE_RIGHTS = 0x2 | 0x4 | 0x10 | 0x40 | 0x100 | 0x10000 | 0x40000 | 0x80000 | 0x10000000 | 0x40000000

# Acl prints a path's descriptor as JSON: owner SID, whether inheritance is blocked, and every rule.
ACL = r'''
function Acl([string]$Path){
 $I=Get-Item -LiteralPath $Path -Force
 $A=if($I.PSIsContainer){[IO.FileSystemAclExtensions]::GetAccessControl([IO.DirectoryInfo]::new($I.FullName))}else{[IO.FileSystemAclExtensions]::GetAccessControl([IO.FileInfo]::new($I.FullName))}
 $Rules=@(foreach($R in $A.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier])){@{sid=$R.IdentityReference.Value;allow=($R.AccessControlType -eq 'Allow');rights=[int]$R.FileSystemRights;inherited=$R.IsInherited}})
 'ACL='+(ConvertTo-Json -Compress -Depth 4 -InputObject @{path=$Path;owner=$A.GetOwner([Security.Principal.SecurityIdentifier]).Value;protected=$A.AreAccessRulesProtected;rules=$Rules})
}
$script:Users=[Security.Principal.SecurityIdentifier]::new('S-1-5-32-545')
'''


def precondition():
    """precondition returns why the tests cannot run here, or an empty string when they can."""
    if os.name != 'nt':
        return 'real Windows security descriptors need Windows'
    if not shutil.which('pwsh'):
        return 'PowerShell 7 (pwsh) is missing'
    probe = subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-Command',
                            '[Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)'],
                           capture_output=True, text=True, timeout=60)
    if probe.stdout.strip() != 'True':
        return 'the process is not elevated'
    return ''


class WindowsTrustedSnapshotTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        reason = precondition()
        if reason and STRICT:
            raise AssertionError('SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1 but ' + reason)
        if reason:
            raise unittest.SkipTest(reason)

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.w = Workspace(directory.name)
        program_data = Path(os.environ.get('ProgramData', r'C:\ProgramData'))
        self.w.base = program_data / ('sandboxed-agents-manual-test-' + uuid.uuid4().hex)
        self.w.root = self.w.base / 'trusted' / RUN_ID
        self.w.output = self.w.base / 'output' / RUN_ID
        self.addCleanup(self.remove, self.w.base)

    def remove(self, path):
        """remove deletes a test base in the elevated test process, unlinking junctions without following them."""
        if not path.exists():
            return
        pwsh("Get-ChildItem -LiteralPath '%s' -Recurse -Force -Attributes ReparsePoint|ForEach-Object{[IO.Directory]::Delete($_.FullName)}\n"
             "Remove-Item -LiteralPath '%s' -Recurse -Force" % (path, path), self.w.d, 'remove.ps1')

    def run_real(self, body, package=None):
        """run_real runs body after a Package.ps1 with the real descriptors and only the trusted base substituted."""
        self.w.save()
        package = package or self.w.package / 'Package.ps1'
        return pwsh(". '%s'\n%s\nfunction Get-TrustedBase{'%s'}\n%s" % (package, ACL, self.w.base, body), self.w.d)

    def acl(self, result):
        """acl returns every descriptor the script printed, in order."""
        return [json.loads(line[4:]) for line in result.stdout.splitlines() if line.startswith('ACL=')]

    def assertNoLine(self, line, result):
        """assertNoLine asserts the script never printed line on a line of its own; a refusal message such
        as REFUSED=... may contain the same letters."""
        self.assertNotIn(line, result.stdout.splitlines(), result.stdout)

    def assertInherits(self, child, parent, reader):
        """assertInherits asserts a file created in an admin-only directory is owned by Administrators
        and carries only rules inherited from that directory, with the same principals and rights, so
        the reader again has no change right."""
        self.assertEqual(child['owner'], 'S-1-5-32-544', child)
        self.assertTrue(child['rules'] and all(r['inherited'] for r in child['rules']), child)
        self.assertEqual(sorted((r['sid'], r['rights'], r['allow']) for r in child['rules']),
                         sorted((r['sid'], r['rights'], r['allow']) for r in parent['rules']), (child, parent))
        self.assertTrue(all(r['rights'] & CHANGE_RIGHTS == 0 for r in child['rules'] if r['sid'] == reader), child)

    def assertAdminOnly(self, descriptor, reader=None):
        """assertAdminOnly asserts a descriptor is owned by Administrators, blocks inheritance, allows
        full control only to Administrators and SYSTEM, and gives a reader, if any, no change right."""
        self.assertEqual(descriptor['owner'], 'S-1-5-32-544', descriptor)
        self.assertTrue(descriptor['protected'], descriptor)
        expected = {'S-1-5-32-544', 'S-1-5-18'} | ({reader} if reader else set())
        self.assertEqual({r['sid'] for r in descriptor['rules']}, expected, descriptor)
        for rule in descriptor['rules']:
            if rule['sid'] == reader:
                self.assertEqual(rule['rights'] & CHANGE_RIGHTS, 0, descriptor)

    def test_the_real_program_data_ancestors_are_accepted(self):
        """The default descriptors of the volume root and the program data folder let the observer start."""
        result = self.run_real("Assert-AncestorsCannotReplace (Join-Path ([Environment]::GetFolderPath('CommonApplicationData')) 'sandboxed-agents-manual')\n'ANCESTORS_OK'")
        self.assertIn('ANCESTORS_OK', result.stdout, result.stdout + result.stderr)

    def test_admin_only_directory_and_file_carry_a_protected_acl(self):
        """New-AdminOnlyDirectory and Protect-TrustedFile write the protected Administrators and SYSTEM descriptor."""
        result = self.run_real(
            "Initialize-TrustedDirectory (Get-TrustedBase)\n$D=Join-Path (Get-TrustedBase) 'd';New-AdminOnlyDirectory $D\n"
            "$F=Join-Path $D 'f.txt';[IO.File]::WriteAllText($F,'x');Protect-TrustedFile $F\n"
            "Acl (Get-TrustedBase);Acl $D;Acl $F\n'ADMIN_ONLY='+((Test-AdminOnlyPath $D) -and (Test-AdminOnlyPath $F))")
        self.assertIn('ADMIN_ONLY=True', result.stdout, result.stdout + result.stderr)
        descriptors = self.acl(result)
        self.assertEqual(len(descriptors), 3)
        for descriptor in descriptors:
            self.assertAdminOnly(descriptor)
            self.assertFalse(any(r['inherited'] for r in descriptor['rules']), descriptor)

    def test_output_directory_is_readable_by_the_item_account_only(self):
        """The output directory grants its reader read access only and stays admin-only, and a file the
        observer writes into it inherits exactly the directory's rules and is admin-only too."""
        result = self.run_real(
            "Initialize-TrustedDirectory (Get-TrustedBase)\n$O=Join-Path (Get-TrustedBase) 'out';New-AdminOnlyDirectory $O '%s'\n"
            "Write-NewJson (Join-Path $O 'capture.ready.json') @{ready=$true}\nAcl $O;Acl (Join-Path $O 'capture.ready.json')\n'ADMIN_ONLY='+(Test-AdminOnlyPath $O)\n'RECORD_ADMIN_ONLY='+(Test-AdminOnlyPath (Join-Path $O 'capture.ready.json'))" % USERS)
        self.assertIn('ADMIN_ONLY=True', result.stdout, result.stdout + result.stderr)
        self.assertIn('RECORD_ADMIN_ONLY=True', result.stdout)
        directory, record = self.acl(result)
        self.assertAdminOnly(directory, USERS)
        self.assertInherits(record, directory, USERS)

    def test_inherited_granted_or_user_owned_access_is_not_admin_only(self):
        """A directory that inherits the program data descriptor, grants Users write, or is owned by the
        person's account is refused, and Initialize-TrustedDirectory never repairs one."""
        result = self.run_real(
            "[void](New-Item -ItemType Directory -Path (Get-TrustedBase))\n'INHERITED='+(Test-AdminOnlyPath (Get-TrustedBase))\n"
            "try{Initialize-TrustedDirectory (Get-TrustedBase);'USED'}catch{'REFUSED='+$_.Exception.Message}\n"
            "$G=Join-Path (Get-TrustedBase) 'granted';[void][IO.FileSystemAclExtensions]::Create([IO.DirectoryInfo]::new($G),(New-AdminOnlySecurity $true))\n"
            "$S=[IO.FileSystemAclExtensions]::GetAccessControl([IO.DirectoryInfo]::new($G));$S.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($script:Users,[Security.AccessControl.FileSystemRights]::Write,[Security.AccessControl.AccessControlType]::Allow));[IO.FileSystemAclExtensions]::SetAccessControl([IO.DirectoryInfo]::new($G),$S)\n"
            "'GRANTED='+(Test-AdminOnlyPath $G)\n"
            "$O=Join-Path (Get-TrustedBase) 'owned';[void][IO.FileSystemAclExtensions]::Create([IO.DirectoryInfo]::new($O),(New-AdminOnlySecurity $true))\n"
            "$S=[IO.FileSystemAclExtensions]::GetAccessControl([IO.DirectoryInfo]::new($O));$S.SetOwner([Security.Principal.WindowsIdentity]::GetCurrent().User);[IO.FileSystemAclExtensions]::SetAccessControl([IO.DirectoryInfo]::new($O),$S)\n"
            "Acl $O\n'OWNED='+(Test-AdminOnlyPath $O)")
        output = result.stdout + result.stderr
        self.assertIn('INHERITED=False', output)
        self.assertIn('REFUSED=Trusted directory', output)
        self.assertIn('not admin-only', output)
        self.assertNoLine('USED', result)
        self.assertIn('GRANTED=False', output)
        self.assertNotEqual(self.acl(result)[0]['owner'], 'S-1-5-32-544', output)
        self.assertIn('OWNED=False', output)

    def test_junctions_are_refused_as_snapshot_and_as_ancestor(self):
        """A junction is never admin-only, an ancestor junction is refused, and a junction at the trusted base is not used."""
        result = self.run_real(
            "Initialize-TrustedDirectory (Get-TrustedBase)\n$T=Join-Path (Get-TrustedBase) 'target';New-AdminOnlyDirectory $T\n"
            "$J=Join-Path (Get-TrustedBase) 'link';[void](New-Item -ItemType Junction -Path $J -Target $T)\n"
            "'JUNCTION='+(Test-AdminOnlyPath $J)\n"
            "try{Assert-AncestorsCannotReplace (Join-Path $J 'child');'ANCESTOR_USED'}catch{'ANCESTOR='+$_.Exception.Message}\n"
            "function Get-TrustedBase{$J}\ntry{Initialize-TrustedDirectory (Get-TrustedBase);'BASE_USED'}catch{'BASE='+$_.Exception.Message}")
        output = result.stdout + result.stderr
        self.assertIn('JUNCTION=False', output)
        self.assertIn('ANCESTOR=Trusted directory ancestor', output)
        self.assertIn('is a reparse point', output)
        self.assertIn('BASE=Trusted directory', output)
        self.assertNoLine('ANCESTOR_USED', result)
        self.assertNoLine('BASE_USED', result)

    def test_ancestor_that_grants_delete_child_is_refused(self):
        """An ancestor that lets Users delete its children is refused, because a child could be renamed away and replaced."""
        result = self.run_real(
            "Initialize-TrustedDirectory (Get-TrustedBase)\n$P=Join-Path (Get-TrustedBase) 'parent';New-AdminOnlyDirectory $P\n"
            "$S=[IO.FileSystemAclExtensions]::GetAccessControl([IO.DirectoryInfo]::new($P));$S.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($script:Users,[Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles,[Security.AccessControl.AccessControlType]::Allow));[IO.FileSystemAclExtensions]::SetAccessControl([IO.DirectoryInfo]::new($P),$S)\n"
            "try{Assert-AncestorsCannotReplace (Join-Path $P 'child');'ANCESTOR_USED'}catch{'ANCESTOR='+$_.Exception.Message}")
        output = result.stdout + result.stderr
        self.assertIn('lets another account replace what lies below it', output)
        self.assertNoLine('ANCESTOR_USED', result)

    def test_the_observer_refuses_the_item_shell_account_and_reads_real_accounts(self):
        """Get-ProcessOwnerSid reads the real account of a process, Assert-SeparateObserver refuses an
        item shell under the observer's own account, and Test-AdministratorAccount sees the
        Administrators SID of this elevated process."""
        result = self.run_real(
            "$Me=Get-ObserverSid\n'SAME='+((Get-ProcessOwnerSid $PID) -ieq $Me)\n'ADMIN='+(Test-AdministratorAccount)\n"
            "$I=[pscustomobject]@{pid=$PID;user_sid=$Me;administrator_account=$false}\n"
            "try{[void](Assert-SeparateObserver $I);'SEPARATE_USED'}catch{'REFUSED='+$_.Exception.Message}")
        output = result.stdout + result.stderr
        self.assertIn('SAME=True', output)
        self.assertIn('ADMIN=True', output)
        self.assertIn("REFUSED=The observer runs under the item shell's account", output)
        self.assertNoLine('SEPARATE_USED', result)

    def test_bound_input_refuses_junctions_links_and_second_names(self):
        """Read-BoundInput reads the bound file and refuses a run directory replaced by a junction, a
        symbolic link at the fixed name, and a file with a second hard link, with real Windows handles."""
        run = self.w.d / 'bound-run'
        elsewhere = self.w.d / 'elsewhere'
        run.mkdir()
        elsewhere.mkdir()
        (run / 'item.saved.json').write_text('{"saved":true}')
        (elsewhere / 'item.saved.json').write_text('{"elsewhere":true}')
        read = "$B=Read-BoundInput $Bound 'item.saved.json'\nif($null -eq $B){'MISSING'}else{'TEXT='+[Text.Encoding]::UTF8.GetString($B)}"
        script = (
            "$Bound=Get-FinalDirectory '%(run)s'\n'BOUND='+$Bound\n" + read + "\n"
            "Remove-Item -LiteralPath '%(run)s\\item.saved.json'\n[void](New-Item -ItemType HardLink -Path '%(run)s\\item.saved.json' -Target '%(elsewhere)s\\item.saved.json')\n"
            "try{" + read + "}catch{'HARDLINK='+$_.Exception.Message}\n"
            "Remove-Item -LiteralPath '%(run)s\\item.saved.json'\n[void](New-Item -ItemType SymbolicLink -Path '%(run)s\\item.saved.json' -Target '%(elsewhere)s\\item.saved.json')\n"
            "try{" + read + "}catch{'SYMLINK='+$_.Exception.Message}\n"
            "Remove-Item -LiteralPath '%(run)s\\item.saved.json'\n[IO.Directory]::Delete('%(run)s')\n[void](New-Item -ItemType Junction -Path '%(run)s' -Target '%(elsewhere)s')\n"
            "try{" + read + "}catch{'JUNCTION='+$_.Exception.Message}\n[IO.Directory]::Delete('%(run)s')"
        ) % {'run': run, 'elsewhere': elsewhere}
        result = self.run_real(script)
        output = result.stdout + result.stderr
        self.assertIn('TEXT={"saved":true}', result.stdout, output)
        self.assertNotIn('elsewhere":true', result.stdout)
        self.assertIn('HARDLINK=Input item.saved.json has more than one name', output)
        self.assertIn('SYMLINK=Input item.saved.json is not the file', output)
        self.assertIn('JUNCTION=Input item.saved.json is not the file', output)

    def test_snapshot_and_output_are_admin_only_and_refused_once_opened_up(self):
        """The observer's sequence writes an admin-only snapshot and a read-only output directory that a
        fresh process accepts, and refuses the snapshot after one file grants Users write access."""
        system32 = Path(os.environ.get('SystemRoot', r'C:\Windows')) / 'System32'
        for name in ('wpr', 'tracerpt'):
            tool = system32 / (name + '.exe')
            if not tool.exists():
                if STRICT:
                    self.fail('SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1 but %s is not in System32 on this machine' % tool.name)
                self.skipTest('%s is not in System32 on this machine' % tool.name)
            self.w.configuration['native_tools'][name] = {'path': str(tool), 'sha256': digest(tool)}
        # Procmon is not installed on the test machine; a Microsoft-signed pwsh.exe stands in for its signature check.
        procmon = self.w.d / 'tools' / 'Procmon64.exe'
        shutil.copyfile(Path(shutil.which('pwsh')).resolve().with_name('pwsh.exe'), procmon)
        self.w.configuration['native_tools']['procmon'] = {'path': str(procmon), 'sha256': digest(procmon)}
        created = self.run_real(
            "$P=Read-Prepared '%s'\n$I=Get-Content -Raw -LiteralPath '%s'|ConvertFrom-Json\n$T=New-TrustedSnapshot $P $I '%s' '%s' '%s'\n'SNAPSHOT='+$T.Root\n"
            "Acl (Get-TrustedBase);Acl (Join-Path (Get-TrustedBase) 'trusted');Acl (Join-Path (Get-TrustedBase) 'output');Acl $T.Root;Acl (Join-Path $T.Root 'Package.ps1');Acl $T.Procmon;Acl $T.OutputDirectory" % (
                self.w.run, self.w.run / 'item-context.private.json', USERS, self.w.run, self.w.package))
        self.assertIn('SNAPSHOT=', created.stdout, created.stdout + created.stderr)
        descriptors = self.acl(created)
        self.assertEqual(len(descriptors), 7)
        for descriptor in descriptors[:-1]:
            self.assertAdminOnly(descriptor)
        self.assertAdminOnly(descriptors[-1], USERS)
        root = self.w.root
        accepted = self.run_real("$T=Read-TrustedSnapshot '%s'\n'TRUSTED_OK='+$T.Procmon\n'OUT='+$T.OutputDirectory\n'RUN='+$T.Bound.run" % root, root / 'Package.ps1')
        self.assertIn('TRUSTED_OK=%s' % (root / 'native' / 'Procmon64.exe'), accepted.stdout, accepted.stdout + accepted.stderr)
        self.assertIn('OUT=%s' % self.w.output, accepted.stdout)
        opened = self.run_real(
            "$F=Join-Path '%s' 'Review-InstallerTrace.ps1';$S=[IO.FileSystemAclExtensions]::GetAccessControl([IO.FileInfo]::new($F));$S.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($script:Users,[Security.AccessControl.FileSystemRights]::Modify,[Security.AccessControl.AccessControlType]::Allow));[IO.FileSystemAclExtensions]::SetAccessControl([IO.FileInfo]::new($F),$S)\n"
            "$T=Read-TrustedSnapshot '%s'\n'TRUSTED_OK'" % (root, root), root / 'Package.ps1')
        self.assertNotIn('TRUSTED_OK', opened.stdout)
        self.assertIn('Trusted snapshot file missing or not admin-only: Review-InstallerTrace.ps1', opened.stdout + opened.stderr)


if __name__ == '__main__':
    unittest.main()
