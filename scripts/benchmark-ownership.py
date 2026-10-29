#!/usr/bin/env python3
"""Measure one supplied image in a fresh rootless Podman store; never alter the active store."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import subprocess
import tempfile
import time

LABEL = 'io.sandboxed-agents.benchmark'


def interrupted(signum, frame):
    """Send SIGTERM through the same owned-process cleanup as an interactive interrupt."""
    raise KeyboardInterrupt(signal.Signals(signum).name)


def process_sample(group):
    """Record state, wait channel and I/O counters only for this command's process group."""
    sample = []
    for path in Path('/proc').glob('[0-9]*'):
        try:
            fields = (path / 'stat').read_text().rsplit(') ', 1)[1].split()
            if int(fields[2]) != group:
                continue
            item = {'pid': int(path.name), 'state': fields[0], 'start_ticks': fields[19]}
            for name in ('wchan', 'io'):
                try:
                    item[name] = (path / name).read_text().strip()
                except OSError as error:
                    item[name] = str(error)
            sample.append(item)
        except (OSError, ValueError, IndexError):
            continue  # A process exiting between directory/stat reads is expected.
    return sample


class Measurement:
    """Run a fixed measurement sequence with bounded commands in one private, new store."""
    def __init__(self, args, baseline):
        self.args = args
        self.directory = Path(tempfile.mkdtemp(prefix='ownership-', dir=args.parent))
        self.report = {'result': 'incomplete', 'baseline': baseline, 'kernel': platform.release(),
                       'image': args.image, 'peak_bytes': args.peak_bytes, 'elapsed_seconds': {},
                       'cleanup': 'not started', 'free_before': shutil.disk_usage(args.parent).free}
        self.environment = dict(os.environ)
        for key in ('CONTAINER_HOST', 'CONTAINER_CONNECTION', 'CONTAINER_SSHKEY', 'STORAGE_OPTS',
                    'CONTAINERS_CONF', 'CONTAINERS_CONF_OVERRIDE', 'CONTAINERS_STORAGE_CONF'):
            self.environment.pop(key, None)
        for name in ('graph', 'run', 'tmp', 'config', 'data', 'runtime'):
            (self.directory / name).mkdir(mode=0o700)
        self.environment.update(TMPDIR=str(self.directory / 'tmp'), XDG_CONFIG_HOME=str(self.directory / 'config'),
                                XDG_DATA_HOME=str(self.directory / 'data'), XDG_RUNTIME_DIR=str(self.directory / 'runtime'))
        storage = self.directory / 'config' / 'storage.conf'
        storage.write_text('[storage]\ndriver = '+json.dumps(args.driver)+'\ngraphroot = '+
                           json.dumps(str(self.directory / 'graph'))+'\nrunroot = '+json.dumps(str(self.directory / 'run'))+'\n')
        self.environment['CONTAINERS_STORAGE_CONF'] = str(storage)
        self.command = ['podman', '--remote=false', '--root', str(self.directory / 'graph'),
                        '--runroot', str(self.directory / 'run'), '--storage-driver', args.driver,
                        '--volumepath', str(self.directory / 'volumes')]
        if args.mount_program:
            self.command += ['--storage-opt', 'overlay.mount_program=' + str(args.mount_program)]
        self.command += ['--tmpdir', str(self.directory / 'tmp')]
        self.report['podman_prefix'] = self.command
        print(self.directory, flush=True)

    def run(self, name, arguments, timeout=30):
        """Time a command separately, sample its local group, and retain all state on interruption."""
        started = time.monotonic()
        with (self.directory / (name + '.stdout')).open('wb') as out, (self.directory / (name + '.stderr')).open('wb') as err:
            process = subprocess.Popen(self.command + arguments, env=self.environment, stdout=out, stderr=err, start_new_session=True)
            try:
                with (self.directory / (name + '.samples.jsonl')).open('w') as samples:
                    while process.poll() is None:
                        free = shutil.disk_usage(self.directory).free
                        samples.write(json.dumps({'elapsed': time.monotonic()-started, 'free_bytes': free,
                                                  'processes': process_sample(process.pid)}) + '\n')
                        samples.flush()
                        if time.monotonic()-started > timeout:
                            raise TimeoutError(name + ' exceeded its command deadline')
                        if free < min(self.args.peak_bytes // 10, 1024**3):
                            raise RuntimeError(name + ' crossed the reserved free-space floor')
                        time.sleep(0.2)
            except BaseException:
                self.report['cleanup'] = 'pending: command interrupted; engine/mapping state must be reconciled before removal'
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.report['owned_process'] = 'still unverified after kill; do not remove store'
                raise
            finally:
                self.report['elapsed_seconds'][name] = round(time.monotonic()-started, 3)
        if process.returncode != 0:
            raise RuntimeError(f'{name} exited {process.returncode}; see {name}.stderr')
        return (self.directory / (name + '.stdout')).read_text()

    def execute(self):
        """Import once, measure keep-id create, validate identity, then remove only proven objects."""
        self.report['podman_version'] = self.run('version', ['--version']).strip()
        info = json.loads(self.run('store-info', ['info', '--format', 'json']))
        store = info['store']
        if not info['host']['security']['rootless'] or Path(store['graphRoot']).resolve() != self.directory / 'graph' or Path(store['runRoot']).resolve() != self.directory / 'run':
            raise RuntimeError('Podman did not select the private rootless store')
        self.report['store'] = store
        self.report['backing_filesystem'] = subprocess.check_output(
            ['findmnt', '--json', '--target', str(self.directory), '--output', 'FSTYPE,SOURCE,OPTIONS'], text=True, timeout=10)
        if json.loads(self.run('before-images', ['images', '--all', '--no-trunc', '--format', 'json'])):
            raise RuntimeError('new store unexpectedly contains images')
        with self.args.archive.open('rb') as archive:
            self.report['archive_sha256'] = hashlib.file_digest(archive, 'sha256').hexdigest()
        self.run('load', ['load', '--input', str(self.args.archive)], self.args.timeout)
        image = json.loads(self.run('loaded-image', ['image', 'inspect', self.args.image]))[0]
        if image['Id'].removeprefix('sha256:') != self.args.image.removeprefix('sha256:'):
            raise RuntimeError('loaded image does not match the supplied full image ID')
        self.report['loaded_image_id'] = image['Id']
        nonce = self.directory.name
        volume = nonce + '-workspace'
        self.run('workspace', ['volume', 'create', '--label', LABEL+'='+nonce, volume])
        check = ('test "$(id -u):$(id -g)" = 0:0; chown 1000:1000 /workspace; '
                 'runuser -u agent -- sh -ec \'test "$(id -u):$(id -g)" = 1000:1000; '
                 'touch /workspace/probe; test "$(stat -c %u:%g /workspace/probe)" = 1000:1000; '
                 'echo identity/workspace:1000:1000\'')
        container = self.run('create', ['create', '--name', nonce, '--label', LABEL+'='+nonce,
            '--pull=never', '--userns=keep-id:uid=1000,gid=1000', '--user=0:0', '--network=none',
            '--security-opt=no-new-privileges', '--pids-limit=256', '--memory=4g', '--memory-swap=4g',
            '--cpus=2', '--mount', 'type=volume,source='+volume+',destination=/workspace',
            '--entrypoint=/bin/sh', self.args.image, '-ec', check], self.args.timeout).strip()
        if not re.fullmatch('[0-9a-f]{64}', container):
            raise RuntimeError('create did not return a full container ID')
        self.report['container_id'] = container
        output = self.run('identity', ['start', '--attach', container], min(self.args.timeout, 60))
        if output.strip() != 'identity/workspace:1000:1000':
            raise RuntimeError('identity/workspace proof missing')
        owned = json.loads(self.run('owned-container', ['container', 'inspect', container]))[0]
        if owned['Config']['Labels'].get(LABEL) != nonce or owned['State']['Running'] or owned['Id'] != container:
            raise RuntimeError('container ownership or stopped state is unverified; retaining it')
        self.run('remove-container', ['rm', container])
        owned_volume = json.loads(self.run('owned-workspace', ['volume', 'inspect', volume]))[0]
        if owned_volume['Labels'].get(LABEL) != nonce:
            raise RuntimeError('workspace ownership is unverified; retaining it')
        self.run('remove-workspace', ['volume', 'rm', volume])
        # The initially empty private store plus successful load/ID check proves this one image.
        # Do not prune parents or delete the store: other imported IDs were not individually proven.
        self.run('remove-image', ['rmi', '--no-prune', self.args.image])
        self.report['retained_images'] = json.loads(self.run('remaining-images', ['images', '--all', '--no-trunc', '--format', 'json']))
        self.report['cleanup'] = 'owned container/workspace/image removed; unproven parents and store directory retained'
        self.report['result'] = 'pass'


def main():
    """Validate explicit inputs/headroom before creating a private benchmark directory."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', type=Path, required=True)
    parser.add_argument('--image', required=True, help='full sha256 image ID from the supplied saved image')
    parser.add_argument('--baseline-info', type=Path, required=True, help='saved podman info JSON from the source store')
    parser.add_argument('--parent', type=Path, required=True, help='existing directory on the filesystem to compare')
    parser.add_argument('--driver', choices=['overlay', 'vfs'], required=True)
    parser.add_argument('--mount-program', type=Path, help='explicit overlay helper; inspect actual store options in report.json')
    parser.add_argument('--peak-bytes', type=int, required=True, help='conservative additional-space bound, including load temp and mapped copy')
    parser.add_argument('--timeout', type=float, default=900, help='seconds per load/create, at most 7200')
    args = parser.parse_args()
    if platform.system() != 'Linux' or os.geteuid() == 0:
        parser.error('requires a non-root Linux user')
    args.archive, args.parent = args.archive.resolve(strict=True), args.parent.resolve(strict=True)
    if not args.archive.is_file() or not args.parent.is_dir() or not re.fullmatch(r'sha256:[0-9a-f]{64}', args.image):
        parser.error('require a local archive, existing parent directory and full sha256 image ID')
    if args.peak_bytes <= 0 or not 0.1 <= args.timeout <= 7200:
        parser.error('require positive peak-bytes and timeout between 0.1 and 7200 seconds')
    if args.mount_program and (args.driver != 'overlay' or not args.mount_program.is_absolute() or not args.mount_program.is_file()):
        parser.error('mount-program requires overlay and an existing absolute helper path')
    source = json.loads(args.baseline_info.read_text())
    if not source['host']['security']['rootless']:
        parser.error('baseline must describe rootless Podman')
    baseline = {key: source['store'].get(key) for key in ('graphRoot', 'runRoot', 'graphDriverName', 'graphOptions', 'graphStatus')}
    for name in ('graphRoot', 'runRoot'):
        if not baseline[name] or args.parent.is_relative_to(Path(baseline[name]).resolve()):
            parser.error('benchmark parent must be outside the active graphroot/runroot')
    if shutil.disk_usage(args.parent).free <= args.peak_bytes:
        parser.error('free space does not exceed the supplied peak bound')
    measurement = Measurement(args, baseline)
    try:
        signal.signal(signal.SIGTERM, interrupted)
        measurement.execute()
    except (Exception, KeyboardInterrupt) as error:
        measurement.report['result'] = 'fail'
        measurement.report['error'] = str(error) or 'interrupted'
        if measurement.report['cleanup'] == 'not started':
            measurement.report['cleanup'] = 'pending: failure retained store; inspect recorded objects before removal'
    finally:
        measurement.report['free_after'] = shutil.disk_usage(args.parent).free
        (measurement.directory / 'report.json').write_text(json.dumps(measurement.report, indent=2)+'\n')
    return 0 if measurement.report['result'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
