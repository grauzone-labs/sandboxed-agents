#!/usr/bin/env python3
"""Exercise the real manager and GCM on a local repository image with dummy secrets only."""
import concurrent.futures
import json
import os
import secrets
import subprocess
import sys
import time
import threading
import uuid

image = sys.argv[1]
name = 'wi143-' + uuid.uuid4().hex[:10]
volume = name + '-home'
manager = '/usr/local/bin/sandboxed-agents-manager'
home = '/home/agent'
state = home + '/.local/share/sandboxed-agents/git-credentials'
gpg = state + '/gnupg'
phrase = secrets.token_urlsafe(28).encode()
token = secrets.token_urlsafe(24).encode()
sampling_done = threading.Event()
samples = []
sample_errors = []


def podman(*args, data=None, ok=True, timeout=35):
    """Run only local Podman, retaining diagnostics in memory rather than printing secret-bearing data."""
    result = subprocess.run(['podman', *args], input=data, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout)
    if ok and result.returncode:
        raise RuntimeError('local Podman operation failed: ' + ' '.join(args[:3]))
    return result


def inside(*args, data=None, ok=True):
    """Run a process as the sandbox's agent without any credential in argv or environment."""
    return podman('exec', '--user=1000:1000', '--env', 'HOME=' + home,
                  '--env', 'GIT_TERMINAL_PROMPT=0', '-i', name, *args, data=data, ok=ok)


def setup(password=phrase, confirm=None, ok=True):
    """Exercise the manager's private stdin handoff after the host terminal guard."""
    return inside(manager, 'integrations', 'config', 'git', 'credentials',
                  data=json.dumps({'Passphrase': password.decode(),
                                   'Confirmation': (password if confirm is None else confirm).decode()}).encode(), ok=ok)


def fill(host='example.invalid', username='dummy', ok=True):
    """Retrieve a dummy credential through real Git and the installed manager helper."""
    return inside('git', 'credential', 'fill',
                  data=f'protocol=https\nhost={host}\nusername={username}\n\n'.encode(), ok=ok)


def locked():
    """Prove an unavailable unlock fails without prompts inside the helper's deadline and grace."""
    start = time.monotonic()
    result = fill(ok=False)
    elapsed = time.monotonic() - start
    assert result.returncode != 0, 'locked lookup succeeded'
    assert elapsed < 12, f'locked lookup exceeded bound: {elapsed:.2f}s'
    print(f'PASS locked lookup: {elapsed:.2f}s', flush=True)


def start():
    """Start a local container with a persistent home and no network; lifecycle equivalence is explicit."""
    podman('run', '-d', '--name', name, '--network=none', '--volume', volume + ':' + home,
           '--entrypoint', 'sleep', image, 'infinity')


def snapshot():
    """Hash managed regular files, excluding Unix sockets and permissions from the byte comparison."""
    return inside('sh', '-c', 'find ' + state + ' -type f -exec sha256sum {} + | sort').stdout


def scan():
    """Inspect the persistent home, logs, and process args/env without transmitting the needle to a child."""
    files = podman('exec', '--user=0:0', name, 'tar', '-cf', '-', home, ok=False).stdout
    logs = podman('logs', name).stdout
    processes = podman('exec', '--user=0:0', name, 'sh', '-c',
                       'for p in /proc/[0-9]*; do cat "$p/cmdline" "$p/environ" 2>/dev/null || :; done').stdout
    for sample in (files, logs, processes):
        assert phrase not in sample, 'test passphrase leaked'
        assert token not in sample, 'test credential leaked'
    print('PASS home/log/process argument and environment secret scan', flush=True)


def sample_processes():
    """Sample args and environments during setup, unlock and lookups, without sending secret needles to children."""
    while not sampling_done.wait(0.1):
        try:
            result = podman('exec', '--user=0:0', name, 'sh', '-c',
                            'for p in /proc/[0-9]*; do cat "$p/cmdline" "$p/environ" 2>/dev/null || :; done', ok=False)
            if result.returncode == 0:
                samples.append(len(result.stdout))
                if phrase in result.stdout or token in result.stdout:
                    sample_errors.append('secret in process arguments or environment')
        except subprocess.TimeoutExpired:
            sample_errors.append('process sampling timed out')


try:
    podman('volume', 'create', volume)
    start()
    sampler = threading.Thread(target=sample_processes, daemon=True)
    sampler.start()
    inside('git', 'init', '/workspace/git-config-proof')
    inside('git', '-C', '/workspace/git-config-proof', 'config', 'credential.helper', '!false')
    repository_config = inside('cat', '/workspace/git-config-proof/.git/config').stdout
    system_config = inside('cat', '/etc/gitconfig', ok=False).stdout
    for password, confirm in [(b'', b''), (phrase, b'different-dummy')]:
        assert setup(password, confirm, ok=False).returncode != 0
    assert inside(manager, '__git-store-state').stdout.strip() == b'new'
    setup()
    assert inside('git', 'config', '--global', '--get-all', 'credential.helper').stdout.strip() == b"!'/usr/local/bin/sandboxed-agents-manager' __git-credential-helper"
    assert inside('git', 'config', '--global', '--get', 'credential.credentialStore').stdout.strip() == b'gpg'
    assert inside('git', 'config', '--global', '--get', 'credential.interactive').stdout.strip() == b'false'
    assert inside('cat', '/workspace/git-config-proof/.git/config').stdout == repository_config
    assert inside('cat', '/etc/gitconfig', ok=False).stdout == system_config
    key_records = inside('gpg', '--homedir', gpg, '--with-colons', '--list-keys').stdout.splitlines()
    primaries = [line.split(b':') for line in key_records if line.startswith(b'pub:')]
    subkeys = [line.split(b':') for line in key_records if line.startswith(b'sub:')]
    assert len(primaries) == len(subkeys) == 1
    assert primaries[0][6] == subkeys[0][6] == b'' and b'e' in subkeys[0][11]
    print('PASS first setup, empty/mismatch refusal, dedicated key, global config, repository/system preservation', flush=True)
    inside('git', 'config', '--global', 'credential.https://example.invalid.provider', 'generic')
    inside('git', 'credential', 'approve', data=b'protocol=https\nhost=example.invalid\nusername=dummy\npassword=' + token + b'\n\n')
    assert b'password=' + token in fill().stdout
    assert inside('test', '!', '-e', home + '/.git-credentials').returncode == 0
    scan()
    assert setup(b'wrong-dummy', ok=False).returncode != 0
    locked()
    setup()
    print('PASS warm-cache wrong passphrase relocks; correct rerun reuses key', flush=True)
    inside('sh', '-c', "printf 'default-cache-ttl 2\\nmax-cache-ttl 2\\n' >> " + gpg + '/gpg-agent.conf')
    inside('gpgconf', '--homedir', gpg, '--kill', 'gpg-agent')
    locked()
    setup()
    time.sleep(5)
    assert b'password=' + token in fill().stdout
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
        assert all(b'password=' + token in result.stdout for result in executor.map(lambda _: fill(), range(8)))
    print('PASS short TTL lifetime and eight concurrent headless lookups', flush=True)
    pid_result = inside('gpg-connect-agent', '--homedir', gpg, 'GETINFO pid', '/bye').stdout
    pid = next(line[2:].decode() for line in pid_result.splitlines() if line.startswith(b'D '))
    inside('kill', '-STOP', pid)
    try:
        locked()
    finally:
        inside('kill', '-CONT', pid)
    inside('kill', '-HUP', pid)
    locked()
    setup()
    print('PASS stalled-agent deadline and SIGHUP relock', flush=True)
    # Wrong recipient and missing private key must not repair or rewrite existing bytes.
    recipient = inside('cat', state + '/password-store/.gpg-id').stdout
    inside('sh', '-c', 'cat > ' + state + '/password-store/.gpg-id', data=b'0' * 40 + b'\n')
    before = snapshot()
    assert setup(ok=False).returncode != 0 and snapshot() == before
    inside('sh', '-c', 'cat > ' + state + '/password-store/.gpg-id', data=recipient)
    print('PASS mismatched recipient refused with byte-identical managed files', flush=True)
    for operation in ('stop/start', 'restart', 'replacement'):
        if operation == 'stop/start':
            podman('stop', '--time', '2', name)
            podman('start', name)
        elif operation == 'restart':
            podman('restart', '--time', '2', name)
        else:
            podman('stop', '--time', '2', name)
            podman('rm', name)
            start()
        locked()
        setup()
        assert b'password=' + token in fill().stdout
        print('PASS lifecycle equivalent: ' + operation, flush=True)
    scan()
    assert not sample_errors, '; '.join(sample_errors)
    print(f'PASS {len(samples)} process argument/environment samples during operations', flush=True)
    print('PASS local mechanism check; lifecycle uses local Podman equivalents, not product lifecycle commands', flush=True)
finally:
    sampling_done.set()
    if 'sampler' in globals():
        sampler.join(timeout=40)
    podman('rm', '--force', name, ok=False)
    podman('volume', 'rm', volume, ok=False)
