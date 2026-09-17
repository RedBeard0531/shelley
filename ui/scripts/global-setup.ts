// Playwright global setup: starts a shelley test server on a random port.
// The actual port is communicated via --port-file, then exported as
// PLAYWRIGHT_TEST_BASE_URL so every worker's baseURL fixture picks it up.

import { execFileSync, execSync, spawn, type ChildProcess } from 'child_process';
import { mkdirSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'fs';
import { tmpdir } from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const shelleyDir = path.resolve(__dirname, '../..');
const binPath = path.join(shelleyDir, 'bin', 'shelley');

let serverProcess: ChildProcess | null = null;
let tempDir: string | null = null;

export default async function globalSetup() {
  // Give every shard its own home and cwd. The tests edit user AGENTS.md,
  // and prompt hydration scans cwd for guidance/skills: sharing the runner's
  // HOME or walking all of /tmp makes unrelated builds interfere.
  tempDir = mkdtempSync(path.join(tmpdir(), 'shelley-e2e-'));
  const cwd = path.join(tempDir, 'cwd');
  const home = path.join(tempDir, 'home');
  mkdirSync(cwd);
  mkdirSync(home);
  process.env.SHELLEY_TEST_CWD = cwd;

  const cleanup = () => {
    if (tempDir) {
      rmSync(tempDir, { recursive: true, force: true });
      tempDir = null;
    }
    delete process.env.SHELLEY_TEST_CWD;
  };

  // External servers keep their own environment; API fixtures still get a
  // small, real directory instead of the shared /tmp tree.
  if (process.env.TEST_SERVER_URL) {
    process.env.PLAYWRIGHT_TEST_BASE_URL = process.env.TEST_SERVER_URL;
    return cleanup;
  }

  // Git-viewer specs need real history, not the runner's giant checkout.
  const git = (...args: string[]) => execFileSync('git', [
    '-C', cwd, '-c', 'user.name=Shelley Test', '-c', 'user.email=test@example.com',
    '-c', 'commit.gpgsign=false', ...args,
  ], { env: { ...process.env, HOME: home } });
  git('init', '--quiet', '--initial-branch=main');
  writeFileSync(path.join(cwd, 'example.txt'), 'before\n');
  git('add', 'example.txt');
  git('commit', '--quiet', '-m', 'Initial test fixture');
  writeFileSync(path.join(cwd, 'example.txt'), 'after\n');
  git('commit', '--quiet', '-am', 'Update test fixture');

  // Build shelley binary if it doesn't exist.
  if (!existsSync(binPath)) {
    console.log('Building shelley binary…');
    execSync('go build -o bin/shelley ./cmd/shelley', {
      cwd: shelleyDir,
      stdio: 'inherit',
    });
  }

  // Database and port file stay outside the conversation working directory.
  const testDb = path.join(tempDir, 'test.db');
  const portFile = path.join(tempDir, 'port');
  const socketPath = path.join(tempDir, 'client.sock');
  process.env.TEST_SERVER_SOCKET = socketPath;

  console.log(`Starting shelley (db=${testDb}, port-file=${portFile})`);

  let earlyExit = false;
  let exitCode: number | null = null;

  serverProcess = spawn(binPath, [
    '--predictable-only',
    '--db', testDb,
    'serve',
    '--port', '0',
    '--port-file', portFile,
    '--socket', socketPath,
  ], {
    cwd,
    stdio: 'inherit',
    env: {
      ...process.env,
      HOME: home,
      PWD: cwd,
      PREDICTABLE_DELAY_MS: process.env.PREDICTABLE_DELAY_MS || '20',
    },
  });

  serverProcess.on('exit', (code) => {
    earlyExit = true;
    exitCode = code;
  });

  // Wait for port file to appear (server has bound).
  const deadline = Date.now() + 30_000;
  while (!existsSync(portFile)) {
    if (earlyExit) {
      throw new Error(`Shelley server exited (code ${exitCode}) before writing port file`);
    }
    if (Date.now() > deadline) {
      throw new Error('Shelley server did not write port file within 30s');
    }
    await new Promise(r => setTimeout(r, 50));
  }

  const port = readFileSync(portFile, 'utf8').trim();
  const baseURL = `http://localhost:${port}`;
  console.log(`Shelley test server listening at ${baseURL}`);

  // Wait for the server to actually respond to HTTP.
  const httpDeadline = Date.now() + 30_000;
  let httpReady = false;
  while (Date.now() < httpDeadline) {
    if (earlyExit) {
      throw new Error(`Shelley server exited (code ${exitCode}) during startup`);
    }
    try {
      const res = await fetch(baseURL);
      if (res.ok) { httpReady = true; break; }
    } catch {
      // not ready yet
    }
    await new Promise(r => setTimeout(r, 100));
  }
  if (!httpReady) {
    throw new Error(`Shelley server at ${baseURL} never responded OK within 30s`);
  }

  // Playwright's built-in baseURL fixture reads this env var.
  process.env.PLAYWRIGHT_TEST_BASE_URL = baseURL;

  // Reap the server before removing files it may still be writing.
  return async () => {
    const server = serverProcess!;
    try {
      if (server.exitCode !== null || server.signalCode !== null) {
        throw new Error(`Shelley exited before teardown (code ${server.exitCode}, signal ${server.signalCode})`);
      }
      const exited = new Promise<void>((resolve) => server.once('exit', () => resolve()));
      let timedOut = false;
      const deadline = setTimeout(() => {
        timedOut = true;
        server.kill('SIGKILL');
      }, 10_000);
      try {
        server.kill('SIGTERM');
        await exited;
        if (timedOut) throw new Error('Shelley did not exit within 10s of SIGTERM');
      } finally {
        clearTimeout(deadline);
      }
    } finally {
      serverProcess = null;
      cleanup();
    }
  };
}
