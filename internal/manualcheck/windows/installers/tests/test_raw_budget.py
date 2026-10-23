"""Exercise Test-RawBudget.ps1, the export's raw-size guard, including the real binary capture of
2026-10-22 (PML 186,724,501 bytes, ETL 148,897,792 bytes) that the previous 128 MiB trace cap
refused although the trace is a complete memory-mode recording with its rundown."""
import json
import subprocess
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / 'Test-RawBudget.ps1'


class RawBudgetTests(unittest.TestCase):
    def budget(self, pml, etl):
        result = subprocess.run(['pwsh', '-NoProfile', '-NonInteractive', '-Command',
                                 f"& '{SCRIPT}' -PmlBytes {pml} -EtlBytes {etl} | ConvertTo-Json -Compress"],
                                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_real_binary_capture_is_exportable(self):
        verdict = self.budget(186724501, 148897792)
        self.assertTrue(verdict['Ok'], verdict['Reasons'])
        self.assertEqual(verdict['RequiredFreeBytes'], (186724501 + 148897792) * 50 + 1024 ** 3)

    def test_trace_above_512_mib_is_refused(self):
        verdict = self.budget(1000, 512 * 1024 ** 2 + 1)
        self.assertFalse(verdict['Ok'])
        self.assertIn('kernel trace exceeds 512 MiB', verdict['Reasons'])

    def test_procmon_above_256_mib_is_refused(self):
        verdict = self.budget(256 * 1024 ** 2 + 1, 1000)
        self.assertFalse(verdict['Ok'])
        self.assertIn('Procmon backing file exceeds 256 MiB', verdict['Reasons'])

    def test_empty_files_are_refused(self):
        verdict = self.budget(0, 0)
        self.assertFalse(verdict['Ok'])
        self.assertEqual(len(verdict['Reasons']), 2)


if __name__ == '__main__':
    unittest.main()
