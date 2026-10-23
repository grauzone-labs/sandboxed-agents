"""Exercise Write-NewJson of Package.ps1, the writer of every evidence file. On 2026-10-22 the
binary capture had no SetEAFile rows, Export-BookkeepingEvidence passed an empty array, the
pipeline form of ConvertTo-Json emitted nothing, and GetBytes(null) threw. The writer must
serialize an empty array as [] and keep a one-element array an array."""
import json
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class WriteNewJsonTests(unittest.TestCase):
    def write(self, expression, twice=False):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'out.json'
            script = Path(directory) / 'run.ps1'
            body = ". '" + str(ROOT / 'Package.ps1') + "'\nWrite-NewJson '" + str(target) + "' " + expression + "\n"
            if twice:
                body += "Write-NewJson '" + str(target) + "' " + expression + "\n"
            script.write_text(body)
            result = subprocess.run(['pwsh', '-NoProfile', '-NonInteractive', '-File', str(script)], capture_output=True, text=True, timeout=30)
            content = target.read_text(encoding='utf-8') if target.exists() else None
            return result, content

    def test_empty_array_is_written_as_json_array(self):
        result, content = self.write('@()')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(content), [])

    def test_single_element_array_stays_an_array(self):
        result, content = self.write("@([pscustomobject]@{file='a.dll';queryExit=0})")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(content), [{'file': 'a.dll', 'queryExit': 0}])

    def test_hashtable_and_nested_depth(self):
        result, content = self.write("@{schema_version=2;artifacts=@{pml=@{path='p';sha256='s'}}}")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(content)['artifacts']['pml']['sha256'], 's')

    def test_existing_file_is_never_replaced(self):
        result, content = self.write("@{a=1}", twice=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads(content), {'a': 1})


if __name__ == '__main__':
    unittest.main()
