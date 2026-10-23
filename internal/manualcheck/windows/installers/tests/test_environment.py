import json
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / 'Review-InstallerEnvironment.ps1'
DEFAULT = r'C:\Users\agent\AppData\Local\Programs\sandboxed-agents'


class EnvironmentTests(unittest.TestCase):
    def review(self, stages, method):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'env.json'
            source.write_text(json.dumps(stages))
            result = subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(SCRIPT), '-Snapshots', str(source), '-Method', method, '-DefaultInstallRoot', DEFAULT], capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(Path(str(source) + '.summary.json').read_text())

    def test_unchanged_environment(self):
        baseline = [r'Path [ExpandString] = %USERPROFILE%\bin;C:\Tools', 'X [String] = untouched']
        report = self.review({stage: baseline for stage in ['start', 'after-install', 'after-reinstall', 'after-remove']}, 'npm')
        self.assertEqual(report['verdict'], 'NO_FINDINGS')

    def test_raw_path_entry_is_added_and_removed_without_expansion(self):
        baseline = [r'Path [ExpandString] = %USERPROFILE%\bin;C:\Tools', 'X [String] = untouched']
        installed = [baseline[0] + ';' + DEFAULT, baseline[1]]
        report = self.review({'start': baseline, 'after-install': installed, 'after-reinstall': installed, 'after-remove': baseline}, 'nuget-default')
        self.assertEqual(report['verdict'], 'NO_FINDINGS')

    def test_outside_value_change_fails(self):
        baseline = ['Path [String] = C:\\Tools', 'X [String] = old']
        changed = ['Path [String] = C:\\Tools', 'X [String] = changed']
        report = self.review({'start': baseline, 'after-install': changed, 'after-reinstall': changed, 'after-remove': baseline}, 'binary')
        self.assertEqual(report['verdict'], 'FAIL')

    def test_newline_does_not_hide_a_changed_snapshot_structure(self):
        report = self.review({'start': ['A [String] = x', 'B [String] = y'], 'after-install': ['A [String] = x\nB [String] = y'], 'after-reinstall': [], 'after-remove': []}, 'binary')
        self.assertIn('Environment changed at after-install', report['problems'])

    def test_path_kind_change_fails_even_if_restored_later(self):
        before = [r'Path [ExpandString] = %USERPROFILE%\bin']
        after = [r'Path [String] = %USERPROFILE%\bin;' + DEFAULT]
        report = self.review({'start': before, 'after-install': after, 'after-reinstall': after, 'after-remove': before}, 'nuget-default')
        self.assertEqual(report['verdict'], 'FAIL')
        self.assertIn('Path registry kind changed', report['problems'])


if __name__ == '__main__':
    unittest.main()
