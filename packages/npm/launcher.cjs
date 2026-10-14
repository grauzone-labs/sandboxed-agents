#!/usr/bin/env node
'use strict';

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const verifiedBinary = require('./verify.cjs');

let binary;
let launchers;
try {
  binary = verifiedBinary();
  launchers = launcherPaths();
} catch (error) {
  console.error(`sandboxed-agents: ${error.message}`);
  process.exit(1);
}
const child = spawn(binary, process.argv.slice(2), {
  stdio: 'inherit',
  env: { ...process.env, SANDBOXED_AGENTS_NPM_LAUNCHERS: JSON.stringify(launchers) },
});
child.on('error', (error) => {
  console.error(`sandboxed-agents: ${error.message}`);
  process.exitCode = 1;
});
child.on('exit', (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exitCode = code;
});

function launcherPaths() {
  const candidates = new Set([__filename, path.resolve(process.argv[1])]);
  // npm creates local launchers beside the package's node_modules directory.
  const directories = [path.resolve(__dirname, '..', '.bin')];
  // Global Windows shims live at PREFIX; global Unix links live at PREFIX/bin.
  directories.push(process.platform === 'win32'
    ? path.resolve(__dirname, '..', '..')
    : path.resolve(__dirname, '..', '..', '..', 'bin'));
  for (const directory of directories) {
    for (const name of ['sandboxed-agents', 'sandboxed-agents.cmd', 'sandboxed-agents.ps1']) {
      candidates.add(path.join(directory, name));
    }
  }
  return [...candidates].filter((name) => {
    try {
      return fs.statSync(name).isFile();
    } catch (error) {
      if (error.code === 'ENOENT' || error.code === 'ENOTDIR') return false;
      throw error;
    }
  });
}
