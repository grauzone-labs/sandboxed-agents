'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');

function verifiedBinary(verifyBothBinaries = false) {
  if (process.arch !== 'x64' || !['linux', 'win32'].includes(process.platform)) {
    throw new Error(`unsupported platform ${process.platform}-${process.arch}; supported platforms: linux-x64 and win32-x64`);
  }
  const name = process.platform === 'win32'
    ? 'sandboxed-agents-windows-amd64.exe'
    : 'sandboxed-agents-linux-amd64';
  const manifest = fs.readFileSync(path.join(__dirname, 'SHA256SUMS'), 'utf8');
  const names = verifyBothBinaries ? ['sandboxed-agents-linux-amd64', 'sandboxed-agents-windows-amd64.exe'] : [name];
  for (const candidate of names) {
    const entries = manifest.split(/\r?\n/).filter((line) => line.endsWith(`  ${candidate}`));
    if (entries.length !== 1 || !/^[0-9a-f]{64}  /.test(entries[0])) {
      throw new Error(`invalid SHA256SUMS entry for ${candidate}`);
    }
    const actual = createHash('sha256').update(fs.readFileSync(path.join(__dirname, candidate))).digest('hex');
    if (actual !== entries[0].slice(0, 64)) {
      throw new Error(`checksum mismatch for ${candidate}; reinstall sandboxed-agents from a trusted package`);
    }
  }
  return path.join(__dirname, name);
}

module.exports = verifiedBinary;

if (require.main === module) {
  try {
    verifiedBinary(true);
  } catch (error) {
    console.error(`sandboxed-agents: ${error.message}`);
    process.exitCode = 1;
  }
}
