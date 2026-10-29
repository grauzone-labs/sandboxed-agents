"""Exercise the benchmark command against an isolated external Podman fixture, never an engine."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'benchmark-ownership.py'
IMAGE = 'sha256:' + 'a' * 64
FAKE = r'''#!/usr/bin/env python3
import json,os,sys,time
from pathlib import Path
a=sys.argv[1:];root=Path(a[a.index('--root')+1]); base=root.parent
assert Path(os.environ['CONTAINERS_STORAGE_CONF']).is_relative_to(base)
assert 'CONTAINERS_CONF' not in os.environ and 'CONTAINERS_CONF_OVERRIDE' not in os.environ
with open(os.environ['BENCH_TEST_LOG'],'a') as f:f.write(json.dumps(a)+'\n')
x=a.index('--tmpdir')+2;c=a[x:]
if c==['--version']:print('podman version 5.7.1')
elif c==['info','--format','json']:print(json.dumps({'host':{'security':{'rootless':True},'kernel':'test'},'store':{'graphRoot':str(root),'runRoot':str(base/'run'),'graphDriverName':'overlay'}}))
elif c[:2]==['images','--all']:print('[]' if not (base/'loaded').exists() else json.dumps([{'Id':'sha256:'+'a'*64}]))
elif c[0]=='load':(base/'loaded').touch();print('Loaded image: fixture')
elif c[:2]==['image','inspect']:print(json.dumps([{'Id':'sha256:'+'a'*64}]))
elif c[:2]==['volume','create']:(base/'label').write_text(c[3].split('=',1)[1]);print(c[-1])
elif c[0]=='create':
 if os.environ.get('BENCH_TEST_STARTED'):Path(os.environ['BENCH_TEST_STARTED']).write_text(str(os.getpid()))
 if os.environ.get('BENCH_TEST_HANG'):time.sleep(10)
 print('b'*64)
elif c[:2]==['start','--attach']:print('identity/workspace:1000:1000')
elif c[:2]==['container','inspect']:print(json.dumps([{'Id':'b'*64,'Config':{'Labels':{'io.sandboxed-agents.benchmark':'foreign' if os.environ.get('BENCH_TEST_BAD_OWNER') else (base/'label').read_text()}},'State':{'Running':False}}]))
elif c[:2]==['volume','inspect']:print(json.dumps([{'Labels':{'io.sandboxed-agents.benchmark':(base/'label').read_text()}}]))
elif c[0]=='rmi':(base/'loaded').unlink()
elif c[0]=='rm' or c[:2]==['volume','rm']:pass
else:print('unexpected '+repr(c),file=sys.stderr);sys.exit(125)
'''


class BenchmarkCommandTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'podman').write_text(FAKE)
        (self.root / 'podman').chmod(0o700)
        (self.root / 'image.tar').write_bytes(b'fixture archive')
        (self.root / 'info.json').write_text(json.dumps({'host': {'security': {'rootless': True}}, 'store': {'graphRoot': str(self.root / 'active'), 'runRoot': str(self.root / 'active-run'), 'graphDriverName': 'overlay'}}))
        self.log = self.root / 'calls.jsonl'
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ['PATH'], BENCH_TEST_LOG=str(self.log))
        self.env.update(CONTAINERS_STORAGE_CONF='/unrelated/storage.conf', CONTAINERS_CONF='/unrelated/containers.conf', CONTAINERS_CONF_OVERRIDE='/unrelated/override.conf')
        self.args = [sys.executable, str(SCRIPT), '--archive', str(self.root / 'image.tar'), '--image', IMAGE, '--baseline-info', str(self.root / 'info.json'), '--parent', str(self.root), '--driver', 'overlay', '--peak-bytes', '1024', '--timeout', '2']

    def run_benchmark(self):
        return subprocess.run(self.args, env=self.env, capture_output=True, text=True, timeout=15)

    def test_measures_create_and_cleans_only_owned_objects(self):
        result = self.run_benchmark()
        self.assertEqual(result.returncode, 0, result.stderr)
        reports = list(self.root.glob('ownership-*/report.json'))
        self.assertEqual(len(reports), 1, result.stdout)
        report = json.loads(reports[0].read_text())
        self.assertEqual(report['result'], 'pass')
        self.assertIn('load', report['elapsed_seconds'])
        self.assertIn('create', report['elapsed_seconds'])
        calls = [json.loads(line) for line in self.log.read_text().splitlines()]
        for call in calls:
            self.assertEqual(call[0], '--remote=false')
            self.assertTrue(Path(call[call.index('--root')+1]).is_relative_to(reports[0].parent))
            self.assertNotIn('prune', call)
            self.assertNotIn('reset', call)
            self.assertNotIn('--force', call)
        create = next(c for c in calls if 'create' in c and 'volume' not in c)
        self.assertIn('--userns=keep-id:uid=1000,gid=1000', create)
        self.assertIn('--pull=never', create)
        self.assertIn('--network=none', create)
        self.assertTrue(any(c[-3:] == ['rmi', '--no-prune', IMAGE] for c in calls))

    def test_refuses_headroom_without_calling_engine(self):
        self.args[self.args.index('--peak-bytes')+1] = str(2**80)
        result = self.run_benchmark()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('free space', result.stderr)
        self.assertFalse(self.log.exists())
        self.assertEqual(list(self.root.glob('ownership-*')), [])

    def test_refuses_active_store_parent(self):
        active = self.root / 'active'
        active.mkdir()
        self.args[self.args.index('--parent')+1] = str(active)
        result = self.run_benchmark()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('outside the active', result.stderr)
        self.assertFalse(self.log.exists())

    def test_timeout_retains_state_without_cleanup_calls(self):
        self.env['BENCH_TEST_HANG'] = '1'
        self.args[self.args.index('--timeout')+1] = '0.3'
        result = self.run_benchmark()
        self.assertNotEqual(result.returncode, 0)
        report = json.loads(next(self.root.glob('ownership-*/report.json')).read_text())
        self.assertIn('create exceeded', report['error'])
        self.assertIn('pending', report['cleanup'])
        self.assertLess(report['elapsed_seconds']['create'], 2)
        calls = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertFalse(any('rm' in c or 'rmi' in c for c in calls))
        self.assertTrue(next(self.root.glob('ownership-*/loaded')).exists())

    def test_foreign_container_label_prevents_cleanup(self):
        self.env['BENCH_TEST_BAD_OWNER'] = '1'
        result = self.run_benchmark()
        self.assertNotEqual(result.returncode, 0)
        report = json.loads(next(self.root.glob('ownership-*/report.json')).read_text())
        self.assertIn('ownership', report['error'])
        calls = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertFalse(any('rm' in c or 'rmi' in c for c in calls))

    def test_failed_process_start_retains_store_without_cleanup(self):
        (self.root / 'podman').write_text('#!/missing/benchmark/interpreter\n')
        result = self.run_benchmark()
        self.assertNotEqual(result.returncode, 0)
        report = json.loads(next(self.root.glob('ownership-*/report.json')).read_text())
        self.assertEqual(report['result'], 'fail')
        self.assertIn('pending', report['cleanup'])
        self.assertFalse(self.log.exists())
        self.assertTrue(next(self.root.glob('ownership-*/graph')).is_dir())

    def test_sigterm_stops_owned_child_and_retains_store(self):
        marker = self.root / 'create-started'
        self.env.update(BENCH_TEST_HANG='1', BENCH_TEST_STARTED=str(marker))
        self.args[self.args.index('--timeout')+1] = '10'
        process = subprocess.Popen(self.args, env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        child = None
        try:
            deadline = time.monotonic()+5
            while not marker.exists() and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertTrue(marker.exists(), 'create did not start')
            child = int(marker.read_text())
            process.terminate()
            process.communicate(timeout=3)
            self.assertNotEqual(process.returncode, 0)
            self.assertFalse(Path('/proc', str(child)).exists(), 'owned create child survived SIGTERM')
            report = json.loads(next(self.root.glob('ownership-*/report.json')).read_text())
            self.assertEqual(report['result'], 'fail')
            self.assertIn('pending', report['cleanup'])
            calls = [json.loads(line) for line in self.log.read_text().splitlines()]
            self.assertFalse(any('rm' in c or 'rmi' in c for c in calls))
        finally:
            if process.poll() is None:
                process.kill()
            process.communicate(timeout=3)
            if child:
                try:
                    os.killpg(child, signal.SIGKILL)
                except ProcessLookupError:
                    pass


if __name__ == '__main__':
    unittest.main()
