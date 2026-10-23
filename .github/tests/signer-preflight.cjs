// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
// Run only as the unprivileged pull_request job; cryptographic fixtures stay private.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { execFileSync, spawnSync } = require('node:child_process');

function summarize(tap, exitCode) {
  const count = name => {
    const matches = [...tap.matchAll(new RegExp(`^# ${name} (\\d+)$`, 'gm'))];
    assert.equal(matches.length, 1, `Missing or repeated ${name} total`);
    return Number(matches[0][1]);
  };
  const result = Object.fromEntries(['tests', 'pass', 'fail', 'cancelled', 'skipped', 'todo'].map(name => [name, count(name)]));
  assert.ok(Object.values(result).every(Number.isSafeInteger));
  assert.equal(exitCode, 0, 'Test process failed');
  assert.ok(result.tests > 0, 'No tests executed');
  assert.equal(result.pass, result.tests, 'Incomplete passing inventory');
  for (const name of ['fail', 'cancelled', 'skipped', 'todo']) assert.equal(result[name], 0, `Nonzero ${name}`);
  return result;
}

function main() {
  assert.equal(process.env.GITHUB_EVENT_NAME, 'pull_request');
  assert.equal(process.env.RUNNER_OS, 'Linux');
  const release = fs.readFileSync('/etc/os-release', 'utf8');
  assert.match(release, /^ID=ubuntu$/m);
  assert.match(release, /^VERSION_ID="24\.04"$/m);
  const event = JSON.parse(fs.readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
  const pr = event.pull_request;
  const oid = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
  assert.ok(Number.isSafeInteger(event.number) && event.number > 0);
  assert.ok(oid(pr.head.sha) && oid(pr.base.sha));
  assert.ok(oid(process.env.GITHUB_WORKFLOW_SHA));
  for (const name of ['GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT']) assert.match(process.env[name], /^[1-9][0-9]*$/);
  for (const name of ['GITHUB_JOB', 'GITHUB_WORKFLOW_REF']) assert.ok(process.env[name]);
  assert.equal(pr.base.repo.full_name, process.env.GITHUB_REPOSITORY);
  const command = (file, args) => execFileSync(file, args, {
    encoding: 'utf8', timeout: 240000, maxBuffer: 4 << 20, stdio: ['pipe', 'pipe', 'pipe'],
  });
  const checkout = command('git', ['rev-parse', 'HEAD']).trim();
  assert.equal(checkout, pr.head.sha, 'Candidate tests must execute the actual PR head');
  const files = ['.github/workflows/commit-signatures-trusted.yml', '.github/workflows/trusted-api-tests.yml',
    '.github/maintainer-signers.json', '.github/signing-keys/kwisatzh.asc',
    ...fs.readdirSync(path.join(__dirname)).filter(name => name.endsWith('.test.cjs')).sort().map(name => `.github/tests/${name}`),
    '.github/tests/signer-preflight.cjs'];
  const sourceFiles = Object.fromEntries(files.map(name => [name,
    createHash('sha256').update(fs.readFileSync(name)).digest('hex')]));
  const openssh = spawnSync('ssh', ['-V'], { encoding: 'utf8', timeout: 5000, stdio: ['pipe', 'pipe', 'pipe'] });
  assert.equal(openssh.status, 0);
  const versions = {
    node: process.version,
    gpg: command('gpg', ['--version']).split('\n')[0],
    openssh: openssh.stderr.trim(),
    ruby: command('ruby', ['--version']).trim(),
    git: command('git', ['--version']).trim(),
  };
  let tap, exitCode = 0;
  try {
    tap = command(process.execPath, ['--test', '--test-reporter=tap', '--test-concurrency=1', ...files.filter(name => name.endsWith('.test.cjs'))]);
  } catch (error) {
    tap = String(error.stdout || '');
    exitCode = Number.isInteger(error.status) ? error.status : 1;
  }
  // Only test totals are published; failures cannot dump signatures or signed payloads.
  const totals = summarize(tap, exitCode);
  for (const [name, expected] of Object.entries(sourceFiles)) {
    assert.equal(createHash('sha256').update(fs.readFileSync(name)).digest('hex'), expected, 'Source changed during tests');
  }
  assert.equal(command('git', ['rev-parse', 'HEAD']).trim(), checkout);
  const record = {
    version: 1, event: 'pull_request', pr: event.number, repository: process.env.GITHUB_REPOSITORY,
    head_sha: pr.head.sha, base_sha: pr.base.sha, base_ref: pr.base.ref, tested_checkout_sha: checkout,
    workflow_ref: process.env.GITHUB_WORKFLOW_REF, workflow_sha: process.env.GITHUB_WORKFLOW_SHA,
    run_id: process.env.GITHUB_RUN_ID, run_attempt: process.env.GITHUB_RUN_ATTEMPT,
    job: process.env.GITHUB_JOB, runner_os: process.env.RUNNER_OS, ubuntu: '24.04',
    versions, source_files_sha256: sourceFiles, totals, conclusion: 'success',
  };
  fs.writeFileSync(path.join(process.env.RUNNER_TEMP, 'signer-preflight.json'), JSON.stringify(record, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
  fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, '```json\n' + JSON.stringify(record, null, 2) + '\n```\n');
  console.log(JSON.stringify(record));
}

module.exports = { summarize };
if (require.main === module) {
  try { main(); } catch {
    console.error('Signer candidate preflight failed; cryptographic fixture details are withheld.');
    process.exitCode = 1;
  }
}
