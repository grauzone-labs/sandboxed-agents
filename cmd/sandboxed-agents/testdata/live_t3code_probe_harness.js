/* Executes the real live probe with only its filesystem and HTTP system boundaries supplied offline. */
const fs = require('node:fs');
const vm = require('node:vm');
const scenario = process.argv[2];
const root = '/home/agent/.local/share/sandboxed-agents/tools/t3code';
const version = '0.17.1';
const status = (parent, uid = '1000 1000 1000 1000', gid = '1000 1000 1000 1000') =>
  `Name:\tt3\nState:\tS (sleeping)\nPPid:\t${parent}\nUid:\t${uid}\nGid:\t${gid}\nNoNewPrivs:\t1\n`;
const header = 'sl local_address rem_address st\n';
const row = address => `0: ${address}:0EBD 00000000:0000 0A\n`;
const files = new Map([
  [root + '/selection.json', JSON.stringify({version, pin: version})],
  [root + '/service/state.json', JSON.stringify({state: 'running', version, supervisor_pid: 200, service_pid: 201})],
  ['/proc/200/status', status(1)], ['/proc/201/status', status(200)], ['/proc/202/status', status(201)],
  ['/proc/net/tcp', header + row('0100007F')],
  ['/proc/net/tcp6', header + row(scenario === 'ipv6-any' ? '00000000000000000000000000000000' : '00000000000000000000000001000000')],
]);
if (scenario === 'ipv4-only') files.set('/proc/net/tcp6', header);
if (scenario === 'ipv6-unavailable') files.delete('/proc/net/tcp6');
if (scenario === 'ipv6-nonloopback') files.set('/proc/net/tcp6', header + row('00000000000000000000000002000000'));
if (scenario === 'unrelated-ipv6') files.set('/proc/net/tcp6', header + row('00000000000000000000000000000000').replace(':0EBD', ':0016'));
if (scenario === 'connected-ipv6') files.set('/proc/net/tcp6', header + row('00000000000000000000000000000000').replace(' 0A', ' 01'));
if (scenario.startsWith('bad-identity-')) {
  const [, , processKind, field, index] = scenario.split('-');
  const values = ['1000', '1000', '1000', '1000'];
  values[Number(index)] = '1001';
  const [pid, parent] = {supervisor: [200, 1], service: [201, 200], child: [202, 201]}[processKind];
  files.set('/proc/' + pid + '/status',
    status(parent, field === 'uid' ? values.join(' ') : undefined, field === 'gid' ? values.join(' ') : undefined));
}
const missing = path => Object.assign(new Error('missing fixture path: ' + path), {code: 'ENOENT'});
const fakeFS = {
  readFileSync(path) { if (!files.has(path)) throw missing(path); return files.get(path); },
  readdirSync(path) { if (path !== '/proc') throw missing(path); return ['200', '201', '202']; },
  readlinkSync(path) { if (path !== '/home/agent/.local/bin/t3') throw missing(path); return root + '/npm/' + version + '/bin/t3'; },
};
const fakeHTTP = {
  get(url, callback) {
    if (url !== 'http://127.0.0.1:3773/') throw new Error('unexpected probe URL');
    callback({resume() {}, statusCode: 200});
    return {setTimeout() {}, on() {}};
  },
};
const script = fs.readFileSync(__dirname + '/live_t3code_probe.js', 'utf8');
try {
  vm.runInNewContext(script, {
    require(name) { if (name === 'node:fs') return fakeFS; if (name === 'node:http') return fakeHTTP; return require(name); },
    process: {argv: ['node', 'running']}, console,
    setTimeout() { throw new Error('unexpected probe retry'); },
  }, {filename: 'live_t3code_probe.js'});
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
