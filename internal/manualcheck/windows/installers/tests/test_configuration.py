"""Exercise Import-InstallerConfiguration of Package.ps1 against a configuration that pins this
package and synthetic native tools; no native program runs."""
import hashlib
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def package_files():
    files = []
    for path in sorted(ROOT.rglob('*')):
        if path.is_file() and path.suffix in ('.ps1', '.cs', '.py', '.wprp') and '__pycache__' not in path.parts:
            files.append({'path': path.relative_to(ROOT).as_posix(), 'sha256': digest(path)})
    return files


class ConfigurationTests(unittest.TestCase):
    def run_import(self, mutate=None, extra_file=None):
        with tempfile.TemporaryDirectory() as directory:
            d = Path(directory)
            tools = {}
            for name in ('procmon', 'wpr', 'tracerpt'):
                tool = d / (name + '.exe')
                tool.write_bytes(('synthetic ' + name).encode())
                tools[name] = {'path': str(tool), 'sha256': digest(tool)}
            configuration = {'schema_version': 1, 'candidate': 'a' * 40, 'criterion_sha256': 'c' * 64,
                             'package_directory': str(ROOT), 'package_files': package_files(), 'native_tools': tools}
            if mutate:
                mutate(configuration)
            (d / 'configuration.json').write_text(json.dumps(configuration))
            if extra_file:
                extra = ROOT / extra_file
                extra.write_text('# not pinned')
            try:
                script = d / 'run.ps1'
                script.write_text(". '" + str(ROOT / 'Package.ps1') + "'\n[void](Import-InstallerConfiguration '" + str(d / 'configuration.json') + "')\n'IMPORT_OK'\n")
                return subprocess.run(['pwsh', '-NoProfile', '-NonInteractive', '-File', str(script)], capture_output=True, text=True, timeout=60)
            finally:
                if extra_file:
                    os.remove(ROOT / extra_file)

    def test_current_package_imports(self):
        result = self.run_import()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('IMPORT_OK', result.stdout)

    def test_changed_package_hash_is_refused(self):
        def mutate(c):
            c['package_files'][0]['sha256'] = '0' * 64
        self.assertNotEqual(self.run_import(mutate).returncode, 0)

    def test_unpinned_package_file_is_refused(self):
        self.assertNotEqual(self.run_import(extra_file='Unpinned-Extra.ps1').returncode, 0)

    def test_changed_native_tool_is_refused(self):
        def mutate(c):
            c['native_tools']['wpr']['sha256'] = '0' * 64
        self.assertNotEqual(self.run_import(mutate).returncode, 0)

    def test_missing_identity_is_refused(self):
        def mutate(c):
            c['candidate'] = 'short'
        self.assertNotEqual(self.run_import(mutate).returncode, 0)


if __name__ == '__main__':
    unittest.main()
