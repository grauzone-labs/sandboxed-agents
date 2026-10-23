"""Exercise the raw-log reader of named-pipe information queries through the real reviewer.

The fixture is the sequence the npm capture of 2026-10-22 showed for one libuv pipe instance: the
server end's CreatePipe, the client CreateFile, a QueryInformationFile of class FilePipeInformation
that Process Monitor prints as <Unknown>, the FSCTL_PIPE_LISTEN, a broken read, a
FilePipeLocalInformation query printed as <Unknown> too, and the close. The synthetic PML, CSV, and
XML carry the same events; every negative case changes exactly one identity, and the row must then
stay unknown. The bytes are never acceptance evidence.
"""
import json
import os
import subprocess
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

import pml_fixture as pml
from test_kernel import CANDIDATE, CRITERION, ROOT, Fixture

INSTANCE = r'\Device\NamedPipe\uv\18446744073709551615-10'
OTHER_INSTANCE = r'\Device\NamedPipe\uv\0-10'
BARE = r'\Device\NamedPipe'
BASE = datetime(2026, 10, 22, 1, 2, 3, tzinfo=timezone.utc)
UNKNOWN_FIRST, UNKNOWN_SECOND = 4, 7  # zero-based row indexes of the two <Unknown> rows after the two base rows


def pipe_rows(pid='10', tid='20', path=INSTANCE):
    """pipe_rows returns the rows of one pipe instance as the npm capture recorded them."""
    specs = [('CreatePipe', 'SUCCESS', 'Minor ID: 0x0, IRP Flags: 0x884, Flags: 0x1'),
             ('CreateFile', 'SUCCESS', 'Desired Access: Generic Read/Write, Write DAC, Disposition: Open, Options: Non-Directory File, Attributes: n/a, ShareMode: None, AllocationSize: n/a, OpenResult: Opened'),
             ('<Unknown>', 'SUCCESS', ''),
             ('FileSystemControl', 'PIPE CONNECTED', 'Control: FSCTL_PIPE_LISTEN'),
             ('ReadFile', 'PIPE BROKEN', 'Offset: 0, Length: 0, Priority: Normal'),
             ('<Unknown>', 'SUCCESS', ''),
             ('CloseFile', 'SUCCESS', ''),
             ('IRP_MJ_CLOSE', 'SUCCESS', '')]
    return [{'Process Name': 'node.exe', 'PID': pid, 'TID': tid, 'Operation': op, 'Path': path, 'Result': res, 'Detail': det} for op, res, det in specs]


class PipeFixture:
    """PipeFixture adds pipe rows and a synthetic PML to the kernel fixture."""

    def __init__(self, directory, extra_rows):
        self.kernel = Fixture(directory)
        self.rows = self.kernel.rows + extra_rows
        self.moments = [BASE + timedelta(milliseconds=i) for i in range(len(self.rows))]
        for row, moment in zip(self.rows, self.moments):
            row['Time of Day'] = pml.time_of_day(moment)
        self.details, self.event_classes, self.event_process_indexes, self.event_tids, self.statuses, self.operations = {}, {}, {}, {}, {}, {}
        self.pml_options = {}
        classes = [pml.FILE_PIPE_INFORMATION, pml.FILE_PIPE_LOCAL_INFORMATION]
        for i, row in enumerate(self.rows):
            if row['Operation'] == '<Unknown>' and row['Path'].startswith(BARE):
                self.details[i] = pml.pipe_query_details(row['Path'], classes[sum(1 for r in self.rows[:i] if r['Operation'] == '<Unknown>') % 2])

    def save(self):
        """save writes the CSV, XML, PML, and evidence bundle and returns the CSV path."""
        self.kernel.rows = self.rows
        self.kernel.pml = pml.rows_to_pml(self.rows, self.moments, self.details, self.event_classes, self.event_process_indexes, self.event_tids, self.statuses, self.operations, **self.pml_options)
        return self.kernel.save()


class PipeQueryTests(unittest.TestCase):
    def run_pipe(self, change=None, rows=None):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        fixture = PipeFixture(directory.name, pipe_rows() if rows is None else rows)
        if change:
            change(fixture)
        csv = fixture.save()
        process = subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File', str(ROOT / 'Review-InstallerTrace.ps1'), '-Csv', str(csv), '-ProcessIds', '10', '-InstallRoot', r'C:\Users\agent\chk-install',
                                  '-BookkeepingCriterion', 'windows-bookkeeping-v1', '-CriterionSha256', CRITERION, '-OutputDirectory', str(fixture.kernel.root / 'review'),
                                  '-KernelTraceEvidence', str(fixture.kernel.root / 'evidence.json'), '-Candidate', CANDIDATE],
                                 text=True, capture_output=True, timeout=60, env={**os.environ, 'TZ': 'UTC'})
        summary = fixture.kernel.root / 'review' / 'procmon.csv.summary.json'
        return process, json.loads(summary.read_text()) if summary.exists() else None, fixture

    def test_real_log_pipe_queries_pass_only_with_the_raw_proof(self):
        process, summary, _ = self.run_pipe()
        self.assertEqual(process.returncode, 0, process.stdout + process.stderr)
        self.assertEqual(summary['pipe_query_proof']['complete'], True, summary['pipe_query_proof'])
        self.assertEqual(summary['pipe_query_proof']['accepted_full_ordinals'], [UNKNOWN_FIRST + 1, UNKNOWN_SECOND + 1])
        self.assertEqual(summary['pipe_query_rows'], 2)
        self.assertEqual(summary['unknown_rows'], 0)
        self.assertEqual(summary['outside_changes'], 0)
        self.assertEqual(summary['verdict'], 'NO_FINDINGS')

        def no_records(f):
            for i in (UNKNOWN_FIRST, UNKNOWN_SECOND):
                f.event_classes[i] = pml.CLASS_FILE
                del f.details[i]
        process, summary, _ = self.run_pipe(no_records)
        self.assertEqual(process.returncode, 0, process.stdout + process.stderr)
        self.assertEqual(summary['pipe_query_proof']['complete'], True)
        self.assertEqual(summary['pipe_query_proof']['records'], 0)
        self.assertEqual(summary['pipe_query_rows'], 0)
        self.assertEqual(summary['unknown_rows'], 2)
        self.assertEqual(summary['verdict'], 'INCOMPLETE')

    def test_utf16_path_representation_runs_the_same_join(self):
        process, summary, _ = self.run_pipe(lambda f: f.details.__setitem__(UNKNOWN_FIRST, pml.pipe_query_details(INSTANCE, pml.FILE_PIPE_INFORMATION, ascii_path=False)))
        self.assertEqual(process.returncode, 0, process.stdout + process.stderr)
        self.assertEqual(summary['pipe_query_rows'], 2)
        self.assertEqual(summary['unknown_rows'], 0)

    def test_each_identity_mismatch_leaves_the_row_unknown(self):
        def status(f): f.statuses[UNKNOWN_FIRST] = 0xC0000008
        def csv_result(f): f.rows[UNKNOWN_FIRST]['Result'] = 'BUFFER OVERFLOW'
        def other_process(f): f.event_process_indexes[UNKNOWN_FIRST] = 900  # the index of the unrelated PID 99
        def other_thread(f): f.event_tids[UNKNOWN_FIRST] = 21
        def later_tick(f): f.moments[UNKNOWN_FIRST] = f.moments[UNKNOWN_FIRST] + timedelta(microseconds=1)
        def other_instance(f): f.details[UNKNOWN_FIRST] = pml.pipe_query_details(OTHER_INSTANCE, pml.FILE_PIPE_INFORMATION)
        def bare_device(f):
            f.rows[UNKNOWN_FIRST]['Path'] = BARE
            f.details[UNKNOWN_FIRST] = pml.pipe_query_details(BARE, pml.FILE_PIPE_INFORMATION)
        def xml_differs(f):
            import copy
            f.kernel.xml_rows = copy.deepcopy(f.rows)
            f.kernel.xml_rows[UNKNOWN_FIRST]['Path'] = OTHER_INSTANCE
        cases = {
            'information class 16': lambda f: f.details.__setitem__(UNKNOWN_FIRST, pml.pipe_query_details(INSTANCE, 16, length=8)),
            'class 23 with length 40': lambda f: f.details.__setitem__(UNKNOWN_FIRST, pml.pipe_query_details(INSTANCE, pml.FILE_PIPE_INFORMATION, length=40)),
            'class 24 with length 8': lambda f: f.details.__setitem__(UNKNOWN_SECOND, pml.pipe_query_details(INSTANCE, pml.FILE_PIPE_LOCAL_INFORMATION, length=8)),
            'sub-operation byte disagrees': lambda f: f.details.__setitem__(UNKNOWN_FIRST, pml.pipe_query_details(INSTANCE, pml.FILE_PIPE_INFORMATION, sub_operation=24)),
            'raw status is a failure': status,
            'csv result is not success': csv_result,
            'raw event belongs to another process': other_process,
            'raw event belongs to another thread': other_thread,
            'raw event time differs by one tick': later_tick,
            'raw path names another instance': other_instance,
            'bare device path': bare_device,
            'xml export disagrees with the csv': xml_differs,
        }
        for name, change in cases.items():
            with self.subTest(case=name):
                process, summary, _ = self.run_pipe(change)
                self.assertEqual(process.returncode, 0, process.stdout + process.stderr)
                self.assertEqual(summary['pipe_query_rows'], 1, summary['pipe_query_proof'])
                self.assertGreaterEqual(summary['unknown_rows'], 1)
                self.assertEqual(len(summary['pipe_query_proof']['rejected']), 1)
                self.assertNotEqual(summary['verdict'], 'NO_FINDINGS')

    def test_a_raw_record_at_another_ordinal_is_rejected_not_moved(self):
        def record_under_read(f):
            f.event_classes[6] = pml.CLASS_IPC
            f.operations[6] = pml.OPERATIONS['QueryInformationFile']
            f.details[6] = pml.pipe_query_details(INSTANCE, pml.FILE_PIPE_INFORMATION)
        process, summary, _ = self.run_pipe(record_under_read)
        self.assertEqual(process.returncode, 0, process.stdout + process.stderr)
        self.assertEqual(summary['pipe_query_proof']['records'], 3)
        self.assertEqual(summary['pipe_query_rows'], 2)
        self.assertEqual([r['full_ordinal'] for r in summary['pipe_query_proof']['rejected']], [7])
        self.assertEqual(summary['unknown_rows'], 0)

    def test_malformed_logs_fail_the_proof_without_acceptance(self):
        cases = {
            'signature': lambda f: f.pml_options.update(signature=b'PMLX'),
            'version': lambda f: f.pml_options.update(version=10),
            'thirty-two bit': lambda f: f.pml_options.update(is64=0),
            'event count': lambda f: f.pml_options.update(event_count=5),
            'truncated': lambda f: f.pml_options.update(truncate_at=pml.HEADER_SIZE + 100),
            'offsets shifted': lambda f: f.pml_options.update(offsets_delta=1),
            'offsets entry size': lambda f: f.pml_options.update(offsets_entry_size=4),
            'event class 7': lambda f: f.event_classes.__setitem__(0, 7),
            'details shorter than the fixed part': lambda f: f.details.__setitem__(UNKNOWN_FIRST, pml.pipe_query_details(INSTANCE, pml.FILE_PIPE_INFORMATION)[:60]),
            'unknown process index': lambda f: f.event_process_indexes.__setitem__(UNKNOWN_FIRST, 777),
        }
        for name, change in cases.items():
            with self.subTest(case=name):
                process, summary, _ = self.run_pipe(change)
                self.assertEqual(process.returncode, 0, process.stdout + process.stderr)
                self.assertEqual(summary['pipe_query_proof']['complete'], False)
                self.assertNotEqual(summary['pipe_query_proof']['failure'], '')
                self.assertEqual(summary['pipe_query_proof']['accepted_full_ordinals'], [])
                self.assertEqual(summary['pipe_query_rows'], 0)
                self.assertEqual(summary['unknown_rows'], 2)
                self.assertEqual(summary['verdict'], 'INCOMPLETE')

    def test_unknown_rows_on_files_never_use_the_pipe_proof(self):
        rows = [{'Process Name': 'node.exe', 'PID': '10', 'TID': '20', 'Operation': '<Unknown>', 'Path': r'C:\outside\file', 'Result': 'SUCCESS', 'Detail': ''},
                {'Process Name': 'node.exe', 'PID': '10', 'TID': '20', 'Operation': '<Unknown>', 'Path': r'C:\Users\agent\chk-install\file', 'Result': 'SUCCESS', 'Detail': ''}]

        def crafted(f):
            f.event_classes[2] = pml.CLASS_IPC
            f.details[2] = pml.pipe_query_details(r'C:\outside\file', pml.FILE_PIPE_INFORMATION)
        process, summary, _ = self.run_pipe(crafted, rows=rows)
        self.assertEqual(process.returncode, 0, process.stdout + process.stderr)
        self.assertEqual(summary['pipe_query_proof']['records'], 1)
        self.assertEqual(summary['pipe_query_rows'], 0)
        self.assertEqual(summary['unknown_rows'], 2)
        self.assertEqual(summary['verdict'], 'INCOMPLETE')


if __name__ == '__main__':
    unittest.main()
