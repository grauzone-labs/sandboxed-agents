"""Exercise the trusted snapshot and the protected output directory of Package.ps1: the admin-only copy
of every helper, the configuration, and Procmon that the elevated observer takes before CAPTURE_READY;
the output directory that receives every elevated write; the run state bound before the workload; and
the reader of what the item shell writes afterwards.

The real functions run unchanged. Only the operating-system boundaries are substituted: the security
descriptor of a path (Get-PathSecurity), the creation of an admin-only directory and the protection of a
file (New-AdminOnlyDirectory, Protect-TrustedFile), the Microsoft signature, the account SIDs, the
trusted base directory, and the System32 directory. The substituted descriptors model the Windows facts
the checks rely on. Read-BoundInput runs against the real file system here, with real links; real
Windows ACLs, links, and handles are exercised by test_trusted_snapshot_windows.py.
"""
import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
RUN_ID = 'npm-test-1'
PERSON = 'S-1-5-21-1-2-3-1001'
OBSERVER = 'S-1-5-21-9-9-9-1002'


def digest(path):
    """digest returns the SHA-256 of a file as lowercase hex."""
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def pinned(package):
    """pinned lists the package files the configuration pins, with their SHA-256."""
    return [{'path': p.relative_to(package).as_posix(), 'sha256': digest(p)}
            for p in sorted(package.rglob('*'))
            if p.is_file() and p.suffix in ('.ps1', '.cs', '.py', '.wprp') and '__pycache__' not in p.parts]


# The descriptor kinds the fake Get-PathSecurity returns, as PowerShell literals. Rights are Windows
# FileSystemRights values: 0x1F01FF FullControl, 0x1200A9 ReadAndExecute, 0x6 create files and
# folders, 0x40 delete child.
DESCRIPTORS = r'''
$script:Admins='S-1-5-32-544';$script:System='S-1-5-18';$script:Users='S-1-5-32-545';$script:Person='%(person)s'
function New-FakeSecurity([string]$Owner,[object[]]$Rules,[bool]$Reparse=$false){[pscustomobject]@{ReparsePoint=$Reparse;Owner=$Owner;Rules=@($Rules)}}
function New-FakeRule([string]$Sid,[int]$Rights,[bool]$InheritOnly=$false){[pscustomobject]@{Sid=$Sid;Allow=$true;Rights=$Rights;InheritOnly=$InheritOnly}}
$script:Kinds=@{
 'admin'=(New-FakeSecurity $script:Admins @((New-FakeRule $script:Admins 0x1F01FF),(New-FakeRule $script:System 0x1F01FF)))
 'admin-readable'=(New-FakeSecurity $script:Admins @((New-FakeRule $script:Admins 0x1F01FF),(New-FakeRule $script:System 0x1F01FF),(New-FakeRule $script:Person 0x1200A9)))
 'system-default'=(New-FakeSecurity $script:System @((New-FakeRule $script:System 0x1F01FF),(New-FakeRule $script:Admins 0x1F01FF),(New-FakeRule $script:Users 0x6),(New-FakeRule $script:Users 0x1F01FF $true)))
 'person-owned'=(New-FakeSecurity $script:Person @((New-FakeRule $script:Person 0x1F01FF)))
 'users-write'=(New-FakeSecurity $script:Admins @((New-FakeRule $script:Admins 0x1F01FF),(New-FakeRule $script:Users 0x2)))
 'reparse'=(New-FakeSecurity $script:Admins @((New-FakeRule $script:Admins 0x1F01FF)) $true)
 'users-delete-child'=(New-FakeSecurity $script:System @((New-FakeRule $script:System 0x1F01FF),(New-FakeRule $script:Users 0x40)))
}
$script:Secure=@{};$script:Readers=@{}
function Get-PathSecurity([string]$Path){$K=[IO.Path]::GetFullPath($Path).TrimEnd([IO.Path]::DirectorySeparatorChar);if(-not $K){$K=[string][IO.Path]::DirectorySeparatorChar};$Kind=$script:Secure[$K];if(-not $Kind){$Kind='system-default'};return $script:Kinds[$Kind]}
function New-AdminOnlyDirectory([string]$Path,[string]$ReaderSid=''){if(Test-Path -LiteralPath $Path){throw "Trusted directory already exists: $Path"};[void](New-Item -ItemType Directory -Path $Path);$K=[IO.Path]::GetFullPath($Path).TrimEnd([IO.Path]::DirectorySeparatorChar);$script:Secure[$K]=$(if($ReaderSid){'admin-readable'}else{'admin'});$script:Readers[$K]=$ReaderSid;if(-not (Test-AdminOnlyPath $Path)){throw "not admin-only after creation: $Path"}}
function Protect-TrustedFile([string]$Path){$script:Secure[[IO.Path]::GetFullPath($Path)]='admin';if(-not (Test-AdminOnlyPath $Path)){throw "not admin-only after protection: $Path"}}
function Test-MicrosoftSignature([string]$Path){$true}
$script:ObserverSid='%(observer)s';$script:ItemOwner='%(person)s'
function Get-ObserverSid{$script:ObserverSid}
function Get-ProcessOwnerSid([int]$ProcessId){$script:ItemOwner}
''' % {'person': PERSON, 'observer': OBSERVER}


def pwsh(text, directory, name='run.ps1'):
    """pwsh runs a PowerShell script and prints a failure as one ERROR= line with the whole message,
    which the default error view would cut short."""
    script = Path(directory) / name
    script.write_text("try {\n%s\n} catch { 'ERROR=' + $_.Exception.Message; exit 1 }\n" % text)
    return subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(script)], capture_output=True, text=True, timeout=120)


class Workspace:
    """Workspace is a synthetic capture host: a copy of this package, a configuration that pins it and
    synthetic native tools, a prepared run with its item context and install root, a trusted base, and
    a System32 directory."""

    def __init__(self, directory):
        # The long, final form of the directory, so that paths compare equal to what Windows reports.
        self.d = Path(directory).resolve()
        self.package = self.d / 'package'
        shutil.copytree(ROOT, self.package, ignore=shutil.ignore_patterns('__pycache__', '*.pyc'))
        self.system32 = self.d / 'Windows' / 'System32'
        self.system32.mkdir(parents=True)
        tools = {}
        for name in ('wpr', 'tracerpt'):
            tool = self.system32 / (name + '.exe')
            tool.write_bytes(('synthetic ' + name).encode())
            tools[name] = {'path': str(tool), 'sha256': digest(tool)}
        (self.d / 'tools').mkdir()
        procmon = self.d / 'tools' / 'Procmon64.exe'
        procmon.write_bytes(b'synthetic procmon')
        tools['procmon'] = {'path': str(procmon), 'sha256': digest(procmon)}
        self.configuration = {'schema_version': 1, 'candidate': 'a' * 40, 'criterion_sha256': 'c' * 64,
                              'package_directory': str(self.package), 'package_files': pinned(self.package),
                              'native_tools': tools, 'min_free_bytes': 1}
        self.base = self.d / 'ProgramData' / 'sandboxed-agents-manual'
        (self.d / 'ProgramData').mkdir()
        self.run = self.d / 'runs' / RUN_ID
        (self.run / 'fake-podman').mkdir(parents=True)
        self.install = self.d / 'install'
        (self.install / 'logs').mkdir(parents=True)
        self.root = self.base / 'trusted' / RUN_ID
        self.output = self.base / 'output' / RUN_ID
        self.item = {'schema_version': 2, 'method': 'npm', 'run_id': RUN_ID, 'nonce': 'n' * 32, 'pid': 4242,
                     'non_elevated': True, 'administrator_account': False, 'install_root': str(self.install),
                     'completion_file': str(self.install / 'logs' / 'method-complete.json'), 'user_sid': PERSON}
        self.setup = ''

    def save(self):
        """save writes the configuration, the prepared run, and the item context, and returns the
        configuration path."""
        path = self.d / 'installers.json'
        path.write_text(json.dumps(self.configuration))
        prepared = {'schema_version': 3, 'run_id': RUN_ID, 'method': 'npm', 'install_root': str(self.install),
                    'configuration': {'path': str(path), 'sha256': digest(path)}, 'candidate': 'a' * 40, 'criterion_sha256': 'c' * 64}
        (self.run / 'prepared.private.json').write_text(json.dumps(prepared))
        (self.run / 'item-context.private.json').write_text(json.dumps(self.item))
        return path

    def boundaries(self, package):
        """boundaries dot-sources a Package.ps1 and substitutes the operating-system boundaries."""
        return ". '%s'\n%s\nfunction Get-TrustedBase{'%s'}\nfunction Get-SystemDirectory{'%s'}\n" % (package, DESCRIPTORS, self.base, self.system32)

    def protected(self):
        """protected marks what the observer created as admin-only, as a new process sees it on Windows:
        the base, both parents, the snapshot and everything in it, and the readable output directory."""
        marks = ["$script:Secure['%s']='admin'" % p for p in [self.base, self.base / 'trusted', self.base / 'output', self.root] + list(self.root.rglob('*'))]
        marks.append("$script:Secure['%s']='admin-readable'" % self.output)
        return '\n'.join(marks) + '\n'

    def run_script(self, body):
        """run_script runs body after the copied Package.ps1, the substituted boundaries, and setup."""
        self.save()
        return pwsh(self.boundaries(self.package / 'Package.ps1') + self.setup + '\n' + body, self.d)

    def run_snapshot(self, body):
        """run_snapshot runs body after the snapshot's own Package.ps1, the protection marks, and setup."""
        return pwsh(self.boundaries(self.root / 'Package.ps1') + self.protected() + self.setup + '\n' + body, self.d, 'snapshot.ps1')

    def snapshot(self):
        """snapshot runs the observer's sequence: Read-Prepared on the run, the item context, the account
        check, and New-TrustedSnapshot."""
        return self.run_script(
            "$P=Read-Prepared '%s'\n$I=Get-Content -Raw -LiteralPath '%s'|ConvertFrom-Json\n$S=Assert-SeparateObserver $I\n"
            "$T=New-TrustedSnapshot $P $I $S '%s' '%s'\n'SNAPSHOT='+$T.Root\n'OUTPUT='+$T.OutputDirectory\n'READER='+$script:Readers[$T.OutputDirectory]\n"
            "'PROCMON='+$T.Procmon\n'TRACERPT='+$T.Tracerpt" % (self.run, self.run / 'item-context.private.json', self.run, self.package))


class TrustedSnapshotTests(unittest.TestCase):
    def workspace(self):
        """workspace returns a fresh synthetic capture host that is removed after the test."""
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        return Workspace(directory.name)

    def value(self, result, key):
        """value returns the KEY= line the script printed, or fails with its output."""
        for line in result.stdout.splitlines():
            if line.startswith(key + '='):
                return line[len(key) + 1:]
        self.fail('missing %s in output:\n%s\n%s' % (key, result.stdout, result.stderr))

    def test_snapshot_copies_and_binds_every_file_it_runs(self):
        """The snapshot holds every pinned file, Procmon, and the configuration, and its manifest binds
        the output directory, the item shell's SID, the run records, and the final run directories."""
        w = self.workspace()
        result = w.snapshot()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        root = Path(self.value(result, 'SNAPSHOT'))
        self.assertEqual(root, w.root)
        for pin in w.configuration['package_files']:
            self.assertEqual(digest(root / pin['path']), pin['sha256'], pin['path'])
        self.assertEqual(Path(self.value(result, 'PROCMON')), root / 'native' / 'Procmon64.exe')
        self.assertEqual(digest(root / 'native' / 'Procmon64.exe'), w.configuration['native_tools']['procmon']['sha256'])
        self.assertEqual(Path(self.value(result, 'TRACERPT')), w.system32 / 'tracerpt.exe')
        self.assertEqual(Path(self.value(result, 'OUTPUT')), w.output)
        self.assertEqual(self.value(result, 'READER'), PERSON)
        manifest = json.loads((root / 'trusted-snapshot.json').read_text())
        self.assertEqual(manifest['run_id'], RUN_ID)
        self.assertEqual(manifest['configuration_sha256'], digest(w.d / 'installers.json'))
        self.assertEqual(digest(root / 'configuration.json'), manifest['configuration_sha256'])
        self.assertEqual(manifest['output_directory'], str(w.output))
        self.assertEqual(manifest['item_sid'], PERSON)
        self.assertEqual(manifest['bound_directories'], {'run': str(w.run), 'fake_podman': str(w.run / 'fake-podman'), 'install_logs': str(w.install / 'logs')})
        self.assertEqual(manifest['prepared']['run_id'], RUN_ID)
        self.assertEqual(manifest['item']['pid'], 4242)

    def test_observer_refuses_to_share_the_item_shell_account(self):
        """The observer refuses the item shell's own account, an item process under another account than
        its context names, and an item account that is or may be an administrator, before anything is created."""
        cases = {
            'same-account': ("$script:ObserverSid='%s'" % PERSON, {}, 'separate administrator account'),
            'other-owner': ("$script:ItemOwner='S-1-5-21-7-7-7-1003'", {}, 'another account than its context'),
            'administrator': ('', {'administrator_account': True}, 'standard user'),
            'unrecorded': ('', {'administrator_account': None}, 'standard user'),
        }
        for name, (setup, item, message) in cases.items():
            with self.subTest(case=name):
                w = self.workspace()
                w.setup = setup
                w.item.update(item)
                result = w.snapshot()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn(message, result.stdout + result.stderr)
                self.assertFalse(w.base.exists())

    def test_preexisting_base_not_created_by_an_administrator_is_refused(self):
        """A trusted base that inherits, grants, or belongs to another account, or is a link, is refused."""
        for kind in ('system-default', 'person-owned', 'users-write', 'reparse'):
            with self.subTest(kind=kind):
                w = self.workspace()
                w.base.mkdir()
                w.setup = "$script:Secure['%s']='%s'" % (w.base, kind)
                result = w.snapshot()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn('is not admin-only', result.stdout + result.stderr)
                self.assertFalse((w.base / 'trusted').exists())
                self.assertFalse((w.base / 'output').exists())

    def test_ancestor_that_lets_users_replace_the_snapshot_is_refused(self):
        """An ancestor that grants delete-child to Users stops the snapshot before anything is created."""
        w = self.workspace()
        w.setup = "$script:Secure['%s']='users-delete-child'" % (w.d / 'ProgramData')
        result = w.snapshot()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('replace', result.stdout + result.stderr)
        self.assertFalse(w.base.exists())

    def test_reparse_point_ancestor_is_refused(self):
        """An ancestor that is a reparse point stops the snapshot before anything is created."""
        w = self.workspace()
        w.setup = "$script:Secure['%s']='reparse'" % (w.d / 'ProgramData')
        result = w.snapshot()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('reparse point', result.stdout + result.stderr)
        self.assertFalse(w.base.exists())

    def test_an_existing_snapshot_or_output_directory_of_the_run_is_refused(self):
        """A second snapshot of the same run, or an output directory that already exists, is refused."""
        w = self.workspace()
        self.assertEqual(w.snapshot().returncode, 0)
        w.setup = w.protected()
        again = w.snapshot()
        self.assertNotEqual(again.returncode, 0, again.stdout)
        self.assertIn('already exists', again.stderr + again.stdout)
        other = self.workspace()
        other.output.mkdir(parents=True)
        other.setup = '\n'.join("$script:Secure['%s']='admin'" % p for p in [other.base, other.base / 'output', other.output])
        result = other.snapshot()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('already exists: %s' % other.output, result.stdout + result.stderr)
        self.assertFalse((other.root / 'trusted-snapshot.json').exists())

    def test_a_source_file_changed_after_the_import_is_not_copied(self):
        """A package file that changes between the import and the snapshot is refused, and no manifest is written."""
        w = self.workspace()
        result = w.run_script(
            "$P=Read-Prepared '%s'\nAdd-Content -LiteralPath '%s' -Value '# changed after the import'\n$I=Get-Content -Raw -LiteralPath '%s'|ConvertFrom-Json\n$T=New-TrustedSnapshot $P $I '%s' '%s' '%s'" % (
                w.run, w.package / 'Export-InstallerCapture.ps1', w.run / 'item-context.private.json', PERSON, w.run, w.package))
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('Export-InstallerCapture.ps1', result.stdout + result.stderr)
        self.assertFalse((w.root / 'trusted-snapshot.json').exists())

    def test_native_tools_outside_system32_are_refused(self):
        """A configuration that pins tracerpt outside System32 is refused."""
        w = self.workspace()
        other = w.d / 'tools' / 'tracerpt.exe'
        other.write_bytes(b'synthetic tracerpt elsewhere')
        w.configuration['native_tools']['tracerpt'] = {'path': str(other), 'sha256': digest(other)}
        result = w.snapshot()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn('System32', result.stdout + result.stderr)

    def test_export_takes_paths_and_run_records_only_from_the_snapshot(self):
        """After the workload rewrites the run's prepared record and item context, the snapshot still
        returns its own Procmon, configuration, prepared record, item context, and output directory."""
        w = self.workspace()
        self.assertEqual(w.snapshot().returncode, 0)
        evil_tool = w.d / 'tools' / 'evil.exe'
        evil_tool.write_bytes(b'not procmon')
        evil = dict(w.configuration)
        evil['native_tools'] = dict(w.configuration['native_tools'], procmon={'path': str(evil_tool), 'sha256': digest(evil_tool)})
        evil_path = w.d / 'evil.json'
        evil_path.write_text(json.dumps(evil))
        prepared = json.loads((w.run / 'prepared.private.json').read_text())
        prepared['configuration'] = {'path': str(evil_path), 'sha256': digest(evil_path)}
        (w.run / 'prepared.private.json').write_text(json.dumps(prepared))
        (w.run / 'item-context.private.json').write_text(json.dumps(dict(w.item, pid=1, install_root=str(w.d))))
        result = w.run_snapshot("$T=Read-TrustedSnapshot '%s'\n'PROCMON='+$T.Procmon\n'CONFIG='+$T.Prepared.configuration.sha256\n'PID='+$T.Item.pid\n'ROOT='+$T.Item.install_root\n'OUT='+$T.OutputDirectory" % w.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(Path(self.value(result, 'PROCMON')), w.root / 'native' / 'Procmon64.exe')
        self.assertEqual(self.value(result, 'CONFIG'), digest(w.d / 'installers.json'))
        self.assertEqual(self.value(result, 'PID'), '4242')
        self.assertEqual(self.value(result, 'ROOT'), str(w.install))
        self.assertEqual(Path(self.value(result, 'OUT')), w.output)

    def test_a_tampered_or_unprotected_snapshot_or_output_is_refused(self):
        """A changed snapshot file, an unprotected snapshot file or output directory, or a directory
        that is not a snapshot is refused by Read-TrustedSnapshot."""
        expected = {'tamper': 'Trusted snapshot file changed', 'unprotected': 'not admin-only', 'output-writable': 'Output directory',
                    'not-a-snapshot': 'Not a trusted snapshot'}
        for change, message in expected.items():
            with self.subTest(change=change):
                w = self.workspace()
                self.assertEqual(w.snapshot().returncode, 0)
                target = w.root
                if change == 'tamper':
                    with open(w.root / 'Review-InstallerTrace.ps1', 'a') as stream:
                        stream.write('\n# changed inside the snapshot\n')
                elif change == 'unprotected':
                    w.setup = "$script:Secure['%s']='users-write'" % (w.root / 'Package.ps1')
                elif change == 'output-writable':
                    w.setup = "$script:Secure['%s']='users-write'" % w.output
                else:
                    target = w.package
                result = w.run_snapshot("$T=Read-TrustedSnapshot '%s'\n'TRUSTED_OK'" % target)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn('TRUSTED_OK', result.stdout)
                self.assertIn(message, result.stdout + result.stderr)


class BoundInputTests(unittest.TestCase):
    """Read-BoundInput and Write-NewBytes against the real file system, real links, and real locks."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.d = Path(directory.name).resolve()
        self.run = self.d / 'run'
        self.run.mkdir()
        (self.run / 'item.saved.json').write_text('{"saved":true}')
        self.elsewhere = self.d / 'elsewhere' / 'item.saved.json'
        self.elsewhere.parent.mkdir()
        self.elsewhere.write_text('{"elsewhere":true}')
        self.out = self.d / 'out'
        self.out.mkdir()

    def read(self, name='item.saved.json'):
        """read reads NAME from the bound run directory through Read-BoundInput."""
        return pwsh(". '%s'\n$B=Read-BoundInput '%s' '%s'\nif($null -eq $B){'MISSING'}else{'TEXT='+[Text.Encoding]::UTF8.GetString($B)}" % (ROOT / 'Package.ps1', self.run, name), self.d)

    def test_the_bound_file_is_read(self):
        """The file at its fixed name in the bound directory is read, and a missing one reads as missing."""
        self.assertIn('TEXT={"saved":true}', self.read().stdout)
        self.assertIn('MISSING', self.read('method-complete.json').stdout)

    def test_a_link_in_place_of_the_file_is_refused(self):
        """A symbolic link at the fixed name is refused without returning the target's bytes."""
        (self.run / 'item.saved.json').unlink()
        os.symlink(self.elsewhere, self.run / 'item.saved.json')
        result = self.read()
        self.assertNotIn('elsewhere":true', result.stdout)
        self.assertIn('is not the file', result.stdout)

    def test_a_replaced_run_directory_is_refused(self):
        """A run directory replaced by a link to another directory is refused, although the name matches."""
        shutil.rmtree(self.run)
        os.symlink(self.elsewhere.parent, self.run)
        result = self.read()
        self.assertNotIn('elsewhere":true', result.stdout)
        self.assertIn('is not the file', result.stdout)

    def test_a_second_name_of_the_file_is_refused(self):
        """A file with a second hard link is refused, so the reader never reads a file that also lives elsewhere."""
        (self.run / 'item.saved.json').unlink()
        os.link(self.elsewhere, self.run / 'item.saved.json')
        result = self.read()
        self.assertNotIn('elsewhere":true', result.stdout)
        self.assertIn('more than one name', result.stdout)

    def test_a_copy_is_written_once(self):
        """Write-NewBytes writes the verified bytes into the output directory once and refuses a second write."""
        script = ". '%s'\n$B=Read-BoundInput '%s' 'item.saved.json'\nWrite-NewBytes '%s' $B\n'FIRST_OK'\nWrite-NewBytes '%s' $B\n'SECOND_OK'" % (
            ROOT / 'Package.ps1', self.run, self.out / 'item.saved.json', self.out / 'item.saved.json')
        result = pwsh(script, self.d)
        self.assertIn('FIRST_OK', result.stdout)
        self.assertNotIn('SECOND_OK', result.stdout)
        self.assertEqual((self.out / 'item.saved.json').read_text(), '{"saved":true}')

    def held(self, body):
        """held runs body while this process holds item.saved.json open exclusively, as a writer that has
        not finished yet holds it."""
        return pwsh(". '%s'\n$H=[IO.File]::Open('%s','Open','ReadWrite','None')\ntry{\n%s\n}finally{$H.Dispose()}" % (ROOT / 'Package.ps1', self.run / 'item.saved.json', body), self.d)

    def test_a_file_still_being_written_is_pending_only_where_asked(self):
        """With -PendingWhileWritten a file another handle still holds reads as not there yet; without it
        the same sharing violation stops the caller."""
        pending = self.held("$B=Read-BoundInput '%s' 'item.saved.json' -PendingWhileWritten\nif($null -eq $B){'PENDING'}else{'READ'}" % self.run)
        self.assertIn('PENDING', pending.stdout, pending.stdout + pending.stderr)
        strict = self.held("$B=Read-BoundInput '%s' 'item.saved.json'\n'READ'" % self.run)
        self.assertNotIn('READ', strict.stdout.splitlines())
        self.assertIn('being used by another process', strict.stdout)

    @unittest.skipIf(hasattr(os, 'geteuid') and os.geteuid() == 0, 'root reads a file without read permission')
    def test_an_access_error_is_never_pending(self):
        """An input the reader may not open stops the caller even with -PendingWhileWritten."""
        (self.run / 'item.saved.json').chmod(0)
        self.addCleanup((self.run / 'item.saved.json').chmod, 0o600)
        result = self.read_pending()
        self.assertNotIn('PENDING', result.stdout.splitlines())
        self.assertIn('ERROR=', result.stdout)

    def read_pending(self):
        """read_pending reads item.saved.json through Read-BoundInput -PendingWhileWritten."""
        return pwsh(". '%s'\n$B=Read-BoundInput '%s' 'item.saved.json' -PendingWhileWritten\nif($null -eq $B){'PENDING'}else{'READ'}" % (ROOT / 'Package.ps1', self.run), self.d)

    def test_native_tools_start_only_from_the_protected_location(self):
        """New-ProtectedStartInfo refuses before Set-ProtectedLocation, and afterwards gives every native
        process the protected directory as working directory, which is also the process's own."""
        script = (". '%s'\ntry{[void](New-ProtectedStartInfo '/bin/true');'STARTED'}catch{'REFUSED='+$_.Exception.Message}\n"
                  "Set-ProtectedLocation '%s'\n$I=New-ProtectedStartInfo '/bin/true'\n'WORKING='+$I.WorkingDirectory\n'PROCESS='+[Environment]::CurrentDirectory") % (ROOT / 'Package.ps1', self.out)
        result = pwsh(script, self.d)
        self.assertIn('REFUSED=Native tools start only from a protected working directory', result.stdout, result.stdout + result.stderr)
        self.assertNotIn('STARTED', result.stdout)
        self.assertIn('WORKING=%s' % self.out, result.stdout)
        self.assertIn('PROCESS=%s' % self.out, result.stdout)

    def test_a_name_with_a_path_is_refused(self):
        """An input name that is not a plain file name is refused."""
        for name in ('../elsewhere/item.saved.json', 'sub/item.saved.json'):
            with self.subTest(name=name):
                self.assertIn('Invalid input name', self.read(name).stdout)


# What runs elevated after CAPTURE_READY must come from the snapshot and write into the output
# directory. The PowerShell parser lists, in source order, every use of $PSScriptRoot, of the
# configuration's native tools, of the item context's completion file, of Read-Prepared, the snapshot,
# bound-input, write-once, and protected-location commands, and every path built from the run
# directory with Join-Path.
AST = r'''
param([string]$Path)
$Ast=[System.Management.Automation.Language.Parser]::ParseFile($Path,[ref]$null,[ref]$null)
$Uses=@()
foreach($N in $Ast.FindAll({param($n) $true},$true)){
 if($N -is [System.Management.Automation.Language.VariableExpressionAst] -and $N.VariablePath.UserPath -eq 'PSScriptRoot'){$Uses+=@{kind='psscriptroot';offset=$N.Extent.StartOffset}}
 elseif($N -is [System.Management.Automation.Language.CommandAst] -and $N.GetCommandName() -eq 'Join-Path' -and $N.CommandElements.Count -gt 1 -and $N.CommandElements[1] -is [System.Management.Automation.Language.VariableExpressionAst] -and $N.CommandElements[1].VariablePath.UserPath -in @('Run','RunDirectory')){$Uses+=@{kind='run-path';offset=$N.Extent.StartOffset}}
 elseif($N -is [System.Management.Automation.Language.CommandAst] -and $N.GetCommandName() -in @('Read-Prepared','New-TrustedSnapshot','Read-TrustedSnapshot','Invoke-RecordedNative','Read-BoundInput','Write-NewBytes','Set-ProtectedLocation','New-ProtectedStartInfo')){$Uses+=@{kind=$N.GetCommandName();offset=$N.Extent.StartOffset;end=$N.Extent.EndOffset;pending=[bool]@($N.CommandElements|Where-Object{$_ -is [System.Management.Automation.Language.CommandParameterAst] -and $_.ParameterName -eq 'PendingWhileWritten'}).Count}}
 elseif($N -is [System.Management.Automation.Language.MemberExpressionAst] -and $N.Member.Extent.Text -in @('native_tools','completion_file')){$Uses+=@{kind=$N.Member.Extent.Text;offset=$N.Extent.StartOffset}}
}
$Uses|Sort-Object {$_.offset}|ConvertTo-Json -Compress -AsArray
'''


class ElevatedLoadOrderTests(unittest.TestCase):
    def uses(self, name, raw=False):
        """uses returns what the AST script lists for one package script, in source order."""
        with tempfile.TemporaryDirectory() as directory:
            script = Path(directory) / 'ast.ps1'
            script.write_text(AST)
            result = subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(script), '-Path', str(ROOT / name)], capture_output=True, text=True, timeout=60)
            self.assertEqual(result.returncode, 0, result.stderr)
            uses = json.loads(result.stdout)
            return uses if raw else [u['kind'] for u in uses]

    def test_the_observer_runs_and_writes_nothing_outside_the_snapshot_after_it(self):
        """After the snapshot the observer uses no package path, configured tool, run-directory path, or
        completion-file path; what the item shell writes is read only through Read-BoundInput."""
        uses = self.uses('Start-InstallerCapture.ps1', raw=True)
        kinds = [u['kind'] for u in uses]
        self.assertEqual(kinds.count('New-TrustedSnapshot'), 1)
        snapshot = uses[kinds.index('New-TrustedSnapshot')]
        # The package and run directories are the snapshot's sources, inside the snapshot command.
        after = [u['kind'] for u in uses if u['offset'] >= snapshot['end']]
        for kind in ('psscriptroot', 'native_tools', 'Read-Prepared', 'run-path', 'completion_file'):
            self.assertNotIn(kind, after)
        self.assertIn('Invoke-RecordedNative', after)
        reads = [u for u in uses if u['kind'] == 'Read-BoundInput']
        self.assertEqual(len(reads), 2)
        # Both reads are the observer's bounded waits, where a file still being written is pending.
        self.assertTrue(all(u['pending'] and u['offset'] >= snapshot['end'] for u in reads))
        before = kinds[:kinds.index('New-TrustedSnapshot')]
        self.assertNotIn('Invoke-RecordedNative', before)
        self.assertNotIn('New-ProtectedStartInfo', before)
        # A protected location is set before the snapshot and the snapshot becomes it right after.
        self.assertEqual(before.count('Set-ProtectedLocation'), 1)
        self.assertEqual(after[0], 'Set-ProtectedLocation')

    def test_the_elevated_export_binds_the_snapshot_before_anything_else(self):
        """The exports set a protected location, read the snapshot, and make it the location before
        anything else; they never use the prepared record, a configured tool, or a path in the run
        directory; and the export reads every item shell file before it writes any copy."""
        for name in ('Export-InstallerCapture.ps1', 'Export-BookkeepingEvidence.ps1'):
            with self.subTest(script=name):
                uses = self.uses(name, raw=True)
                kinds = [u['kind'] for u in uses]
                for kind in ('Read-Prepared', 'native_tools', 'run-path', 'completion_file'):
                    self.assertNotIn(kind, kinds)
                first = kinds.index('Read-TrustedSnapshot')
                # Before it, only the script's own Package.ps1 is dot-sourced from $PSScriptRoot.
                self.assertEqual([k for k in kinds[:first] if k != 'psscriptroot'], ['Set-ProtectedLocation'])
                # Right after it, apart from its own $PSScriptRoot argument, the snapshot becomes the location.
                self.assertEqual([k for k in kinds[first + 1:] if k != 'psscriptroot'][0], 'Set-ProtectedLocation')
                self.assertFalse(any(u['pending'] for u in uses if u['kind'] == 'Read-BoundInput'))
        uses = self.uses('Export-InstallerCapture.ps1', raw=True)
        reads = [u['offset'] for u in uses if u['kind'] == 'Read-BoundInput']
        writes = [u['offset'] for u in uses if u['kind'] == 'Write-NewBytes']
        # Two call sites: the three saved files in one loop, and the fake Podman calls.
        self.assertEqual(len(reads), 2)
        self.assertTrue(writes and max(reads) < min(writes))


if __name__ == '__main__':
    unittest.main()
