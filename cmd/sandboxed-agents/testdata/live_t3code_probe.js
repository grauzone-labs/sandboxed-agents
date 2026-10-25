/* Live-suite observations and reversible failure injection, confined to its disposable sandbox.
   Only selected service metadata is printed; account data and process environments are never read. */
const fs = require('node:fs');
const http = require('node:http');
const assert = require('node:assert/strict');
const root = '/home/agent/.local/share/sandboxed-agents/tools/t3code';
const link = '/home/agent/.local/bin/t3';
const mode = process.argv[1];
const read = path => JSON.parse(fs.readFileSync(path, 'utf8'));
const exists = path => { try { fs.lstatSync(path); return true; } catch (e) { if (e.code === 'ENOENT') return false; throw e; } };
if (mode === 'break-install') {
  const selection = read(root + '/selection.json');
  const marker = root + '/home/runtime/versions/' + selection.version + '/.install-complete';
  assert(!exists(marker + '.live-held'));
  fs.renameSync(marker, marker + '.live-held');
  fs.chmodSync(root + '/npm', 0o500);
} else if (mode === 'repair-install') {
  fs.chmodSync(root + '/npm', 0o700);
  const selection = read(root + '/selection.json');
  const marker = root + '/home/runtime/versions/' + selection.version + '/.install-complete';
  if (exists(marker + '.live-held')) fs.renameSync(marker + '.live-held', marker);
} else if (mode === 'deleted') {
  assert(!exists(root), 'Tool directory remains');
  assert(!exists(link), 'command link remains');
  for (const pid of process.argv.slice(2).map(Number)) {
    if (!exists('/proc/' + pid + '/status')) continue;
    const status = fs.readFileSync('/proc/' + pid + '/status', 'utf8');
    assert(/^State:\s+Z/m.test(status), 'Tool process still alive');
  }
  console.log(JSON.stringify({deleted: true}));
} else {
  assert.equal(mode, 'running');
  const observe = httpStatus => {
    const selection = read(root + '/selection.json');
    const state = read(root + '/service/state.json');
    assert.equal(state.state, 'running');
    assert.equal(state.version, selection.version);
    assert.equal(fs.readlinkSync(link), root + '/npm/' + selection.version + '/bin/t3');
    const processes = new Map();
    for (const pid of fs.readdirSync('/proc').filter(name => /^\d+$/.test(name))) {
      try { processes.set(Number(pid), fs.readFileSync('/proc/' + pid + '/status', 'utf8')); }
      catch (e) { if (e.code !== 'ENOENT' && e.code !== 'ESRCH') throw e; }
    }
    const pids = new Set([state.supervisor_pid, state.service_pid]);
    let changed = true;
    while (changed) {
      changed = false;
      for (const [pid, status] of processes) {
        const parent = Number(status.match(/^PPid:\s+(\d+)/m)[1]);
        if (pids.has(parent) && !pids.has(pid)) { pids.add(pid); changed = true; }
      }
    }
    for (const pid of pids) {
      assert(Number.isInteger(pid) && pid > 1, 'missing service pid');
      const status = processes.get(pid);
      assert(status && !/^State:\s+Z/m.test(status), 'service process is absent or a zombie');
      assert(/^Uid:\s+1000\s+1000\s+1000\s+1000$/m.test(status), 'Tool uid differs from 1000');
      assert(/^Gid:\s+1000\s+1000\s+1000\s+1000$/m.test(status), 'Tool gid differs from 1000');
      assert(/^NoNewPrivs:\s+1$/m.test(status), 'Tool can gain privileges');
    }
    const listeners = fs.readFileSync('/proc/net/tcp', 'utf8').trim().split('\n').slice(1)
      .map(line => line.trim().split(/\s+/)).filter(fields => fields[3] === '0A' && fields[1].endsWith(':0EBD'));
    assert.equal(listeners.length, 1, 'expected one IPv4 listener on port 3773');
    assert.equal(listeners[0][1], '0100007F:0EBD', 'service is not bound to loopback');
    return {version: selection.version, channel: selection.channel || '', pin: selection.pin || '',
      supervisor_pid: state.supervisor_pid, service_pid: state.service_pid, pids: [...pids], uid: 1000, gid: 1000,
      origin: 'http://127.0.0.1:3773', http_status: httpStatus};
  };
  const deadline = Date.now() + 15000;
  const probe = () => {
    const request = http.get('http://127.0.0.1:3773/', response => {
      response.resume();
      console.log(JSON.stringify(observe(response.statusCode)));
    });
    request.setTimeout(1000, () => request.destroy(new Error('service HTTP timeout')));
    request.on('error', error => { if (Date.now() >= deadline) throw error; setTimeout(probe, 100); });
  };
  probe();
}
