"""Exercise the real Get-OwnedProcess identity guard of Package.ps1.

Only the two operating-system metadata boundaries, Get-Process and Get-CimInstance, are
substituted; the guard's own comparison code runs unchanged. The positive case is the one the
2026-10-22 host probe recorded: the item shell's recorded command line names the managed entry
point pwsh.dll while Win32_Process reports the native host pwsh.exe, with every argument equal.
Every other case must still throw, so the translation never weakens the PID, path, start-ticks,
CIM-presence, or argument guards.
"""
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
NATIVE = r'C:\Program Files\PowerShell\7\pwsh.exe'
MANAGED = r'C:\Program Files\PowerShell\7\pwsh.dll'
ARGUMENTS = r' -NoLogo -NoProfile -NoExit -File "C:\package\Installer-ItemShell.ps1"'


class IdentityTests(unittest.TestCase):
    def run_guard(self, recorded_command=None, live_command=None, live_path=NATIVE, recorded_path=NATIVE,
                  live_ticks_delta=0, cim_present=True, recorded_pid=10, live_pid=10):
        if recorded_command is None:
            recorded_command = '"' + MANAGED + '"' + ARGUMENTS
        if live_command is None:
            live_command = '"' + NATIVE + '"' + ARGUMENTS
        script = ". '" + str(ROOT / 'Package.ps1') + "'\n" + r'''
$script:Live=[pscustomobject]@{Id=%(live_pid)d;Path='%(live_path)s';StartTime=[DateTime]::new(2026,10,22,1,2,3,[DateTimeKind]::Utc).AddTicks(%(delta)d)}
$script:Native=@'
%(live_command)s
'@
function Get-Process {param($Id,$ErrorAction);if($Id -ne $script:Live.Id){throw "no process $Id"};return $script:Live}
function Get-CimInstance {param($ClassName,$Filter);if(-not $%(cim)s){return $null};return [pscustomobject]@{CommandLine=$script:Native}}
$I=[pscustomobject]@{pid=%(recorded_pid)d;path='%(recorded_path)s';start_ticks=[DateTime]::new(2026,10,22,1,2,3,[DateTimeKind]::Utc).Ticks;command_line=@'
%(recorded_command)s
'@}
[void](Get-OwnedProcess $I)
'GUARD_PASS'
''' % dict(live_pid=live_pid, live_path=live_path, delta=live_ticks_delta, live_command=live_command,
           cim='true' if cim_present else 'false', recorded_pid=recorded_pid, recorded_path=recorded_path,
           recorded_command=recorded_command)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'run.ps1'
            path.write_text(script)
            return subprocess.run(['pwsh', '-NoProfile', '-NonInteractive', '-File', str(path)], capture_output=True, text=True, timeout=30)

    def assert_pass(self, result):
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn('GUARD_PASS', result.stdout)

    def assert_reject(self, result):
        self.assertNotEqual(result.returncode, 0, 'the guard accepted a changed identity: ' + result.stdout)
        self.assertIn('Owned process identity changed', result.stderr + result.stdout)
        self.assertNotIn('GUARD_PASS', result.stdout)

    def test_same_shell_managed_entrypoint_passes(self):
        self.assert_pass(self.run_guard())

    def test_identical_native_command_lines_pass(self):
        self.assert_pass(self.run_guard(recorded_command='"' + NATIVE + '"' + ARGUMENTS))

    def test_changed_argument_is_rejected(self):
        self.assert_reject(self.run_guard(live_command='"' + NATIVE + '"' + ARGUMENTS.replace('Installer-ItemShell', 'Other-Shell')))

    def test_extra_argument_is_rejected(self):
        self.assert_reject(self.run_guard(live_command='"' + NATIVE + '"' + ARGUMENTS + ' -Command x'))

    def test_argument_case_change_is_rejected(self):
        self.assert_reject(self.run_guard(live_command='"' + NATIVE + '"' + ARGUMENTS.replace('-NoProfile', '-noprofile')))

    def test_changed_executable_path_is_rejected(self):
        self.assert_reject(self.run_guard(live_path=r'C:\Other\pwsh.exe'))

    def test_changed_start_ticks_is_rejected(self):
        self.assert_reject(self.run_guard(live_ticks_delta=1))

    def test_missing_cim_metadata_is_rejected(self):
        self.assert_reject(self.run_guard(cim_present=False))

    def test_other_dll_first_token_is_not_translated(self):
        self.assert_reject(self.run_guard(recorded_command='"' + r'C:\Program Files\PowerShell\7\other.dll' + '"' + ARGUMENTS))

    def test_managed_dll_in_another_directory_is_not_translated(self):
        self.assert_reject(self.run_guard(recorded_command='"' + r'C:\Elsewhere\pwsh.dll' + '"' + ARGUMENTS))

    def test_unquoted_managed_token_is_not_translated(self):
        self.assert_reject(self.run_guard(recorded_command=MANAGED + ARGUMENTS))

    def test_non_pwsh_native_path_gets_no_translation(self):
        native = r'C:\Tools\Procmon64.exe'
        self.assert_reject(self.run_guard(live_path=native, recorded_path=native,
                                          recorded_command='"' + r'C:\Tools\Procmon64.dll' + '" /AcceptEula',
                                          live_command='"' + native + '" /AcceptEula'))

    def test_non_pwsh_identical_command_passes(self):
        native = r'C:\Tools\Procmon64.exe'
        self.assert_pass(self.run_guard(live_path=native, recorded_path=native,
                                        recorded_command='"' + native + '" /AcceptEula /Quiet',
                                        live_command='"' + native + '" /AcceptEula /Quiet'))

    def test_bare_pwsh_argument_zero_is_rejected(self):
        # A launcher that starts "pwsh ..." without the full path produces this native line; the
        # guard must not accept it, so launchers use the resolved full path as argument zero.
        self.assert_reject(self.run_guard(live_command='pwsh' + ARGUMENTS))

    def test_translation_does_not_hide_a_native_dll_command(self):
        # The live process really runs with a .dll first token: the translated expectation must not match it.
        self.assert_reject(self.run_guard(live_command='"' + MANAGED + '"' + ARGUMENTS))

    def test_item_shell_never_queries_the_elevated_procmon(self):
        # A normal shell cannot read an elevated process's metadata; Begin and Invoke check the
        # observer's ready record only. The source is the seam: no Get-OwnedProcess call remains.
        text = (ROOT / 'Installer-ItemShell.ps1').read_text()
        self.assertNotIn('Get-OwnedProcess', text)
        self.assertEqual(text.count("Test-InstallerReady.ps1') -Record $Ready -Context $V2Context"), 2)


if __name__ == '__main__':
    unittest.main()
