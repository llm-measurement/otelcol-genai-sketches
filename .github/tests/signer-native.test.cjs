// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
// No Git repositories or pre-existing private keys: fixtures live only in a disposable directory.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { createHash } = require('node:crypto');
const { test, after } = require('node:test');
const vm = require('node:vm');
const workflow = JSON.parse(execFileSync('ruby', ['-ryaml', '-rjson', '-e',
  'puts JSON.generate(YAML.safe_load(File.read(ARGV[0])))', path.join(__dirname, '../workflows/commit-signatures-trusted.yml')], { encoding: 'utf8' }));
const script = workflow.jobs['commit-signatures-trusted'].steps[0].with.script;
const verify = vm.runInNewContext(`(async function(github, context, core, require, Buffer) {${script}\n})`);
const noHeadRuleScript = script.replace(' || item.sha === before.head.sha', '');
assert.ok(noHeadRuleScript !== script, 'Head negative-control substitution must apply');
const noHeadRuleVerify = vm.runInNewContext(`(async function(github, context, core, require, Buffer) {${noHeadRuleScript}\n})`);
// Focused negative control: replace ONLY the per-commit guard with the superseded net-tree guard.
const scanStart = script.indexOf('// Scan every introduced commit:');
const scanEnd = script.indexOf('const { data: baseFiles }', scanStart);
assert.ok(scanStart > 0 && scanEnd > scanStart);
const netOnlyScript = script.slice(0, scanStart) +
  'const protectedChange = await protectedEntries(await protectedTree(mergeBase)) !== await protectedEntries(await protectedTree(before.head.sha));\n' + script.slice(scanEnd);
const netOnlyVerify = vm.runInNewContext(`(async function(github, context, core, require, Buffer) {${netOnlyScript}\n})`);
// Negative control for P2-MERGE-DISCARD: restore only the unsafe first-parent equality exemption.
const mergeGuardStart = script.indexOf('const parentStates = [];');
const mergeGuardEnd = script.indexOf('let trustedImport = false;', mergeGuardStart);
assert.ok(mergeGuardStart > 0 && mergeGuardEnd > mergeGuardStart);
const firstParentOnlyScript = script.slice(0, mergeGuardStart) +
  'const first = commit.parents[0]; if (first && state === await protectedEntries(await protectedTree(first.sha))) continue;\n' +
  script.slice(mergeGuardEnd);
const firstParentOnlyVerify = vm.runInNewContext(`(async function(github, context, core, require, Buffer) {${firstParentOnlyScript}\n})`);
const scratch = fs.mkdtempSync('/tmp/collector-crypto-');
fs.chmodSync(scratch, 0o700);
const nativeGPG = execFileSync('/usr/bin/which', ['gpg'], { encoding: 'utf8' }).trim();
const nativeSSH = execFileSync('/usr/bin/which', ['ssh-keygen'], { encoding: 'utf8' }).trim();
const fixtureEnv = { PATH: process.env.PATH, HOME: scratch, GNUPGHOME: scratch, LANG: 'C', LC_ALL: 'C' };
const native = (command, args, input) => execFileSync(command, args, {
  input, env: fixtureEnv, encoding: 'utf8', timeout: 15000, maxBuffer: 262144, stdio: ['pipe', 'pipe', 'pipe'],
});
after(() => {
  for (const home of fixtureHomes) {
    try { native('gpgconf', ['--homedir', home, '--kill', 'gpg-agent']); } catch { /* Already stopped. */ }
  }
  fs.rmSync(scratch, { recursive: true, force: true });
});
const fixtureHomes = [];
function gpgKey(label) {
  const home = fs.mkdtempSync(path.join(scratch, 'key-'));
  fixtureHomes.push(home);
  const args = ['--homedir', home, '--no-options', '--batch', '--pinentry-mode', 'loopback', '--passphrase', ''];
  native(nativeGPG, [...args, '--quick-generate-key', `${label} <${label}@example.test>`, 'ed25519', 'sign', '1d']);
  const rows = native(nativeGPG, [...args, '--with-colons', '--list-keys']).split('\n').map(row => row.split(':'));
  const primary = rows.find(row => row[0] === 'fpr')[9];
  return { primary, home, args, armor: native(nativeGPG, [...args, '--armor', '--export', primary]),
    sign: (payload, signer = primary) => native(nativeGPG, [...args, '--local-user', `${signer}!`, '--armor', '--detach-sign', '--output', '-'], payload) };
}
const maintainer = gpgKey('maintainer-fixture');
native(nativeGPG, [...maintainer.args, '--quick-add-key', maintainer.primary, 'ed25519', 'sign', '1d']);
maintainer.armor = native(nativeGPG, [...maintainer.args, '--armor', '--export', maintainer.primary]);
const subkey = native(nativeGPG, [...maintainer.args, '--with-colons', '--with-subkey-fingerprint', '--list-keys'])
  .split('\n').map(line => line.split(':')).filter(line => line[0] === 'fpr').at(-1)[9];
const outsider = gpgKey('outsider-fixture');
const sshFile = path.join(scratch, 'ssh');
native(nativeSSH, ['-q', '-t', 'ed25519', '-N', '', '-f', sshFile]);
const sshPublic = fs.readFileSync(`${sshFile}.pub`, 'utf8').trim();
const otherSSHFile = path.join(scratch, 'other-ssh');
native(nativeSSH, ['-q', '-t', 'ed25519', '-N', '', '-f', otherSSHFile]);
const otherSSHPublic = fs.readFileSync(`${otherSSHFile}.pub`, 'utf8').trim();
const sshSign = (payload, namespace = 'git') => {
  const file = path.join(scratch, 'ssh-payload');
  fs.writeFileSync(file, payload, { mode: 0o600 });
  fs.rmSync(`${file}.sig`, { force: true });
  native(nativeSSH, ['-Y', 'sign', '-n', namespace, '-f', sshFile, file]);
  return fs.readFileSync(`${file}.sig`, 'utf8');
};
const sha = n => n.toString(16).padStart(40, '0');
const digest = (type, bytes) => createHash('sha1').update(Buffer.concat([
  Buffer.from(`${type} ${bytes.length}\0`), bytes])).digest('hex');
const sha256 = value => createHash('sha256').update(value).digest('hex');
const B = sha(9000), M = sha(8000), tree = sha(5000);
const owner = { id: 123, login: 'contributor' };
const rootOwner = { id: 5549790, login: 'kwisatzh' };
function fixture({ kind = 'gpg', root = false, merge = false, message = 'Change', authorEmail, namespace,
  state = 0, parents: specifiedParents } = {}) {
  const email = root ? 'maintainer@example.test' : 'outsider@example.test';
  const parents = specifiedParents || (merge ? [sha(90), B] : [sha(90)]);
  const commitTree = sha(5000 + state);
  const payload = `tree ${commitTree}\n${parents.map(value => `parent ${value}\n`).join('')}author Fixture <${authorEmail || email}> 1792700000 +0000\ncommitter Fixture <${email}> 1792700000 +0000\n\n${message}\n`;
  const signature = kind === 'ssh' ? sshSign(payload, namespace) : (root ? maintainer : outsider).sign(payload);
  const value = { payload, signature, parents, authorEmail: authorEmail || email, email,
    actor: root ? rootOwner : owner, authorActor: root ? rootOwner : owner, tree: commitTree };
  rehash(value);
  return value;
}
function rehash(value) {
  const boundary = value.payload.indexOf('\n\n');
  const object = `${value.payload.slice(0, boundary)}\ngpgsig ${value.signature.replace(/\n$/, '').replace(/\n/g, '\n ')}${value.payload.slice(boundary)}`;
  value.sha = digest('commit', Buffer.from(object));
  return object;
}
const gpg = fixture(), ssh = fixture({ kind: 'ssh' }), root = fixture({ root: true });
const gpgMerge = fixture({ merge: true }), sshMerge = fixture({ kind: 'ssh', merge: true });
const rootMerge = fixture({ root: true, merge: true });
const policy = { version: 1, maintainers: [{ ...rootOwner, emails: ['maintainer@example.test'], keys: [{
  path: '.github/signing-keys/kwisatzh.asc', sha256: sha256(maintainer.armor),
  primary: maintainer.primary, signers: [maintainer.primary],
}] }] };

async function check(options = {}) {
  const values = structuredClone(options.commits || [root]);
  const before = { base: { sha: B, ref: options.baseRef || 'main' },
    head: { sha: values.at(-1)?.sha, ref: 'contribution' }, commits: options.count ?? values.length };
  options.before?.(before);
  const event = { number: 7, head: { ...before.head }, base: { ...before.base } };
  options.event?.(event);
  const context = { repo: { owner: 'fixture', repo: 'collector' }, sha: sha(9999), payload: { pull_request: event } };
  const afterValue = structuredClone(before); options.after?.(afterValue);
  const files = new Map([
    ['.github/maintainer-signers.json', Buffer.from(JSON.stringify(options.policy || policy))],
    ['.github/signing-keys/kwisatzh.asc', Buffer.from(maintainer.armor)],
  ]);
  const baseEntries = [...files].map(([file, bytes]) => ({ path: file.slice(8), sha: digest('blob', bytes), type: 'blob', mode: '100644' }));
  options.entries?.(baseEntries);
  const calls = { reads: 0, pages: [], blobs: [], full: [], native: [], registry: [], trees: [], accounts: 0 };
  const full = value => {
    const verification = { verified: true, payload: value.payload, signature: value.signature };
    const result = { sha: value.sha, author: value.authorActor, committer: value.actor,
      commit: { verification, author: { email: value.authorEmail }, committer: { email: value.email } } };
    options.full?.(result, value);
    return result;
  };
  const raw = value => {
    const result = { sha: value.sha, tree: { sha: value.tree }, parents: value.parents.map(value => ({ sha: value })),
      verification: { verified: true, payload: value.payload, signature: value.signature } };
    options.raw?.(result, value);
    return result;
  };
  const github = { rest: {
    pulls: { get: async () => {
      calls.reads++;
      if (options.readError === calls.reads) throw new Error('PR metadata unavailable');
      return { data: calls.reads === 1 ? before : afterValue };
    },
      listCommits: () => { throw new Error('Mutable list forbidden'); } },
    repos: {
      compareCommits: async args => {
        if (!('page' in args)) {
          assert.equal(args.head, B); assert.ok([sha(90), sha(91)].includes(args.base));
          return { data: { base_commit: { sha: args.base }, merge_base_commit: { sha: options.badProvenance ? M : args.base }, status: 'ahead' } };
        }
        assert.equal(args.base, B); assert.equal(args.head, before.head.sha);
        assert.equal(args.per_page, 100); assert.equal(args.page, calls.pages.length + 1);
        calls.pages.push(args.page);
        if (options.pageError === args.page) throw new Error('Comparison unavailable');
        const data = { total_commits: before.commits, base_commit: { sha: B }, merge_base_commit: { sha: M },
          commits: values.slice((args.page - 1) * 100, args.page * 100).map(value => ({ sha: value.sha })) };
        options.page?.(data, args.page);
        return { data };
      },
      getContent: async args => {
        assert.equal(args.ref, B); calls.blobs.push(args.path);
        if (options.missingBase && args.path.endsWith('.json')) throw new Error('Missing base policy');
        const bytes = files.get(args.path);
        assert.ok(bytes, 'Only fixed base policy/key paths may be read');
        const data = { type: 'file', encoding: 'base64', size: bytes.length,
          content: bytes.toString('base64'), sha: digest('blob', bytes) };
        options.blob?.(data);
        return { data };
      },
      getCommit: async args => {
        calls.full.push(args.ref);
        if (options.fullError) throw new Error('Full commit unavailable');
        const value = values.find(value => value.sha === args.ref);
        assert.ok(value); return { data: full(value) };
      },
    },
    git: {
      getCommit: async args => {
        const value = values.find(value => value.sha === args.commit_sha);
        if (value) return { data: raw(value) };
        assert.ok([B, M, sha(90), sha(91)].includes(args.commit_sha));
        return { data: { sha: args.commit_sha, parents: [],
          tree: { sha: args.commit_sha === B ? sha(10000) : args.commit_sha === sha(91) ? sha(10003) : sha(10001) } } };
      },
      getTree: async args => {
        calls.trees.push(args.tree_sha);
        if (args.recursive) {
          assert.equal(args.tree_sha, sha(11000));
          return { data: { sha: args.tree_sha, truncated: false, tree: baseEntries } };
        }
        if (parseInt(args.tree_sha, 16) >= 11000 && parseInt(args.tree_sha, 16) < 11020) {
          const introduced = parseInt(args.tree_sha, 16) >= 11002;
          let state = args.tree_sha === sha(11000) ? (options.baseState ?? (options.stale ? 1 : 0))
            : introduced ? parseInt(args.tree_sha, 16) - 11002 : 0;
          if (introduced && options.protected) state = 1;
          const entries = [{ path: 'workflows', type: 'tree', mode: '040000', sha: sha(12000 + (options.protectedKind === 'policy' ? 0 : state)) },
            { path: 'signing-keys', type: 'tree', mode: '040000', sha: sha(13000) },
            { path: 'maintainer-signers.json', type: 'blob', mode: '100644', sha: sha(14000 + (options.protectedKind === 'policy' ? state : options.policyChange && introduced ? 1 : 0)) },
            { path: 'ISSUE_TEMPLATE', type: 'tree', mode: '040000', sha: sha(options.template && introduced ? 15001 : 15000) }];
          if (options.dropPolicy && introduced && state === 1) entries.splice(2, 1);
          return { data: { sha: args.tree_sha, truncated: false, tree: entries } };
        }
        const treeNumber = parseInt(args.tree_sha, 16);
        const subtree = args.tree_sha === sha(10003) && options.firstParentNew ? sha(11003)
          : args.tree_sha === sha(10000) ? sha(11000) : [sha(10001), sha(10003)].includes(args.tree_sha)
          ? sha(11001) : sha(11002 + treeNumber - 5000);
        return { data: { sha: args.tree_sha, truncated: false,
          tree: [{ path: '.github', type: 'tree', mode: '040000', sha: subtree }] } };
      },
    },
    users: { getByUsername: async args => {
      calls.accounts++;
      return { data: { id: options.wrongAccount ? 999 : owner.id, login: args.username } };
    } },
  }, request: async (route, args) => {
    calls.registry.push(route);
    assert.ok(['GET /users/{username}/gpg_keys', 'GET /users/{username}/ssh_signing_keys'].includes(route));
    assert.equal(args.username, owner.login); assert.equal(args.page, 1);
    return { data: options.registry || (route.endsWith('/gpg_keys') ? [{ raw_key: outsider.armor }] : [{ key: sshPublic }]) };
  } };
  const requireNative = name => {
    assert.ok(['node:child_process', 'node:fs', 'node:os', 'node:path', 'node:crypto'].includes(name));
    if (name === 'node:os') return { tmpdir: () => '/tmp' };
    if (name !== 'node:child_process') return require(name);
    return { execFileSync: (command, args, settings) => {
      calls.native.push(command);
      assert.ok(['gpg', 'ssh-keygen'].includes(command));
      assert.equal(settings.env.PATH, '/usr/bin:/bin');
      assert.deepEqual(Object.keys(settings.env).sort(), ['GNUPGHOME', 'HOME', 'LANG', 'LC_ALL', 'PATH']);
      assert.equal(settings.timeout, 5000); assert.equal(settings.maxBuffer, 262144);
      assert.ok(settings.cwd.startsWith('/tmp/collector-signers-'));
      if (command === 'gpg') {
        for (const flag of ['--no-options', '--batch', '--no-tty', '--no-autostart', '--disable-dirmngr', '--no-auto-key-retrieve', '--no-auto-key-import']) assert.ok(args.includes(flag));
        if (options.mockCrypto && args.includes('--verify')) {
          const current = values.find(value => value.sha === calls.full.at(-1));
          const signer = options.mockSigner || (current.actor.id === rootOwner.id ? maintainer.primary : outsider.primary);
          return `[GNUPG:] NEWSIG\n[GNUPG:] VALIDSIG ${signer} 2026-10-22 1792700000 0 4 0 22 8 00 ${signer}\n`;
        }
      } else {
        assert.equal(args[0], '-Y'); assert.equal(args[1], 'verify');
        assert.equal(args[args.indexOf('-n') + 1], 'git');
        assert.equal(args[args.indexOf('-I') + 1], 'collector-contributor');
        const allowed = fs.readFileSync(args[args.indexOf('-f') + 1], 'utf8');
        assert.match(allowed, /^collector-contributor (ssh-|ecdsa-|sk-)/);
      }
      // Production keeps Ubuntu's fixed PATH. Tests select installed native tools on macOS.
      if (command === 'gpg' && args.includes('--verify') && options.status) return options.status;
      return execFileSync(command === 'gpg' ? nativeGPG : nativeSSH, [...args], { ...settings });
    } };
  };
  const info = [], errors = [];
  let thrown;
  try { await (options.noHeadRuleControl ? noHeadRuleVerify : options.firstParentOnlyControl ? firstParentOnlyVerify : options.netOnlyControl ? netOnlyVerify : verify)(github, context,
    { info: value => info.push(value), error: value => errors.push(value) }, requireNative, Buffer); }
  catch (error) { thrown = error; }
  return { passed: !thrown, thrown, calls, info, errors };
}

// Contributor cases retain their own signatures under an otherwise valid head.
// Restrict commit mutations to the earlier fixtures so a bad head cannot mask
// the earlier validation rule being tested.
async function checkEarlier(options = {}) {
  const earlier = options.commits || [gpg];
  assert.ok(earlier.length);
  const last = earlier.at(-1);
  const final = fixture({ root: true, parents: [last.sha],
    state: parseInt(last.tree, 16) - 5000, message: 'Approve preceding contribution' });
  const shas = new Set(earlier.map(value => value.sha));
  const scoped = { ...options, commits: [...earlier, final] };
  for (const name of ['full', 'raw']) if (options[name]) {
    scoped[name] = (result, value) => {
      if (shas.has(value.sha)) options[name](result, value);
    };
  }
  return check(scoped);
}

for (const [label, value] of [['outside GPG', gpg], ['outside SSH', ssh], ['base-allowlisted GPG', root],
  ['two-parent outside GPG merge', gpgMerge], ['two-parent outside SSH merge', sshMerge], ['two-parent maintainer merge', rootMerge]]) {
  test(`native: ${label} passes before a maintainer head, independently hashed without Git writes`, async () => {
    assert.equal(native('git', ['hash-object', '-t', 'commit', '--stdin'], rehash(structuredClone(value))).trim(), value.sha);
    const result = await checkEarlier({ commits: [value] });
    assert.equal(result.passed, true, result.thrown?.message);
    assert.equal(result.calls.reads, 2);
    assert.ok(result.calls.native.includes(value.signature.includes('SSH') ? 'ssh-keygen' : 'gpg'));
  });
}
for (const value of [gpg, ssh]) {
  const type = value === gpg ? 'GPG' : 'SSH';
  test(`native: stale base ${type} ordinary contribution remains valid`, async () => assert.equal((await checkEarlier({ commits: [value], stale: true })).passed, true));
  test(`native: unrelated .github issue template accepts ${type}`, async () => assert.equal((await checkEarlier({ commits: [value], template: true })).passed, true));
  for (const option of ['protected', 'policyChange']) {
    test(`native: ${type} outsider cannot introduce ${option}`, async () => assert.equal((await checkEarlier({ commits: [value], [option]: true })).passed, false));
  }
  test(`native: ${type} tampered signed payload is rejected even with rebound commit SHA`, async () => {
    const changed = structuredClone(value); changed.payload += 'tamper'; rehash(changed);
    assert.equal((await checkEarlier({ commits: [changed] })).passed, false);
  });
  test(`native: ${type} maintainer-looking author refuses own outsider key`, async () => {
    const changed = structuredClone(value); changed.authorActor = rootOwner;
    assert.equal((await checkEarlier({ commits: [changed] })).passed, false);
  });
}
test('native: SSH wrong namespace is rejected', async () => assert.equal((await checkEarlier({ commits: [fixture({ kind: 'ssh', namespace: 'not-git' })] })).passed, false));
test('native: all protected-change commits need old-base authorization, not just a maintainer head', async () => {
  assert.equal((await check({ commits: [gpg, rootMerge], protected: true })).passed, false);
  assert.equal((await check({ commits: [root, rootMerge], protected: true })).passed, true);
});
test('native: missing base trust root has no head/default fallback', async () => {
  const result = await check({ commits: [root], missingBase: true });
  assert.equal(result.passed, false); assert.equal(result.calls.native.length, 0);
});
test('native: maintainer primary/subkey requires exact allowlist membership', async () => {
  const changed = structuredClone(policy); changed.maintainers[0].keys[0].signers = ['A'.repeat(40)];
  assert.equal((await check({ policy: changed, commits: [root] })).passed, false);
});
test('native: public_key is not a raw GPG key fallback', async () => assert.equal((await checkEarlier({ registry: [{ public_key: outsider.armor }] })).passed, false));
test('native: unregistered SSH key cannot authorize a commit', async () => assert.equal((await checkEarlier({ commits: [ssh], registry: [] })).passed, false));
test('native: wrong registered SSH key cannot verify the signature', async () => assert.equal((await checkEarlier({ commits: [ssh], registry: [{ key: otherSSHPublic }] })).passed, false));
test('native: foreign registered GPG certificate cannot verify the signature', async () => assert.equal((await checkEarlier({ registry: [{ raw_key: maintainer.armor }] })).passed, false));
test('native: allowed_signers options/injected principals are not accepted', async () => assert.equal((await checkEarlier({ commits: [ssh], registry: [{ key: `cert-authority ${sshPublic}\n* ${sshPublic}` }] })).passed, false));
test('native: wrong account numeric identity fails despite matching login', async () => assert.equal((await checkEarlier({ wrongAccount: true })).passed, false));
for (const field of ['author', 'committer']) {
  test(`maintainer ${field} id/login forces base key`, async () => {
    assert.equal((await checkEarlier({ full: result => { result[field] = rootOwner; } })).passed, false);
  });
}
test('native: known maintainer email forces base key even without linked author', async () => {
  const value = fixture({ authorEmail: 'maintainer@example.test' }); value.authorActor = null;
  assert.equal((await checkEarlier({ commits: [value] })).passed, false);
});
for (const field of ['author', 'committer']) {
  test(`native: uppercase policy email still requires base key for unlinked ${field}`, async () => {
    const changed = structuredClone(policy);
    changed.maintainers[0].emails = ['MAINTAINER@EXAMPLE.TEST'];
    const value = fixture({ authorEmail: field === 'author' ? 'Maintainer@Example.Test' : undefined });
    value.authorActor = null;
    if (field === 'committer') {
      value.email = 'Maintainer@Example.Test';
      value.payload = value.payload.replace('committer Fixture <outsider@example.test>', `committer Fixture <${value.email}>`);
      value.signature = outsider.sign(value.payload);
      rehash(value);
    }
    assert.equal((await checkEarlier({ commits: [value], policy: changed })).passed, false);
  });
}
for (const value of [false, 'true', 1, null, undefined]) {
  test(`verified must be boolean true: ${String(value)}`, async () => assert.equal((await check({ raw: raw => { raw.verification.verified = value; } })).passed, false));
}
for (const field of ['payload', 'signature']) {
  test(`API ${field} mismatch fails closed`, async () => assert.equal((await check({ raw: raw => { raw.verification[field] += '\n'; } })).passed, false));
}
test('native: verified signature cannot be swapped onto another object SHA', async () => {
  const changed = structuredClone(gpg); changed.sha = sha(37);
  assert.equal((await checkEarlier({ commits: [changed] })).passed, false);
});
test('extra commit headers fail explicitly rather than reconstructing lossy data', async () => {
  const changed = structuredClone(gpg); changed.payload = changed.payload.replace('\n\n', '\nencoding ISO-8859-1\n\n'); rehash(changed);
  assert.equal((await checkEarlier({ commits: [changed] })).passed, false);
});
test('unknown signature types fail closed', async () => {
  const changed = structuredClone(gpg); changed.signature = '-----BEGIN SIGNED MESSAGE-----\nunknown\n'; rehash(changed);
  assert.equal((await checkEarlier({ commits: [changed] })).passed, false);
});
test('web-flow actor is rejected even when API says verified', async () => assert.equal((await check({ full: full => { full.committer = { id: 19864447, login: 'web-flow' }; } })).passed, false));
for (const fpr of ['968479A1AFF927E37D1A566BB5690EEEBB952194', '5DE3E0509C47EA3CF04A42D34AEE18F83AFDEB23']) {
  test(`hard deny web-flow fingerprint ${fpr}`, async () => {
    const changed = structuredClone(policy); changed.maintainers[0].keys[0].primary = fpr;
    assert.equal((await check({ policy: changed })).passed, false);
  });
}
test('symlink trust data fails closed', async () => assert.equal((await check({ entries: entries => { entries[0].mode = '120000'; } })).passed, false));
test('blob hash mismatch fails before any native invocation', async () => assert.equal((await check({ blob: blob => { blob.sha = sha(12); } })).passed, false));
test('final base/head read occurs after native verification and detects movement', async () => {
  const result = await check({ after: after => { after.base.sha = sha(1); } });
  assert.equal(result.passed, false); assert.ok(result.calls.native.length > 0); assert.equal(result.calls.reads, 2);
});

// Envelope regressions use an explicit fake verifier only to keep 250-commit tests bounded.
const many = Array.from({ length: 250 }, (_, index) => {
  const value = structuredClone(gpg); value.payload += String(index); rehash(value); return value;
});
const paginationHeads = new Map();
function pageCommits(count) {
  if (!paginationHeads.has(count)) paginationHeads.set(count, fixture({ root: true,
    parents: count > 1 ? [many[count - 2].sha] : [sha(90)], message: `Approve ${count} pagination fixtures` }));
  return [...many.slice(0, count - 1), paginationHeads.get(count)];
}
for (const count of [1, 100, 101, 200, 201, 250]) {
  test(`immutable pagination: ${count} commits, no mutable API`, async () => {
    const result = await check({ commits: pageCommits(count), mockCrypto: true });
    assert.equal(result.passed, true, result.thrown?.message);
    assert.equal(result.calls.pages.length, Math.ceil(count / 100));
    assert.equal(result.calls.full.length, count);
    assert.equal(result.calls.accounts, count > 1 ? 1 : 0, 'Earlier contributor login/id is looked up once per run');
  });
}
for (const count of [0, -1, 1.5, '1', 251]) {
  test(`invalid count ${count} cannot enumerate`, async () => {
    const result = await check({ count, mockCrypto: true }); assert.equal(result.passed, false); assert.equal(result.calls.pages.length, 0);
  });
}
for (const [label, mutate] of [
  ['base', data => { data.base_commit.sha = sha(1); }],
  ['merge base missing', data => { delete data.merge_base_commit; }],
  ['merge base changes across pages', data => { data.merge_base_commit.sha = sha(1); }],
  ['total', data => { data.total_commits--; }],
  ['short page', data => { data.commits.pop(); }],
  ['duplicate', data => { data.commits[0].sha = many[0].sha; }],
]) {
  test(`pagination rejects inconsistent ${label}`, async () => assert.equal((await check({ commits: pageCommits(250), mockCrypto: true,
    page: (data, page) => { if (page === 2) mutate(data); } })).passed, false));
}
test('ABA pagination cannot omit invalid early commit; no mutable enumeration', async () => {
  const result = await check({ commits: pageCommits(101), mockCrypto: true,
    raw: (raw, value) => { if (value.sha === many[0].sha) raw.verification.verified = false; } });
  assert.equal(result.passed, false); assert.equal(result.calls.pages.length, 2);
});
test('bad third-page commit cannot hide behind valid head', async () => assert.equal((await check({ commits: pageCommits(250), mockCrypto: true,
  raw: (raw, value) => { if (value.sha === many[200].sha) raw.verification.verified = false; } })).passed, false));
test('maintenance target policy uses B, not context/default SHA', async () => {
  const result = await check({ baseRef: 'release/0.6', commits: [root] });
  assert.equal(result.passed, true); assert.equal(result.calls.blobs.length, 2);
});
test('production seed matches reviewed public-key hash and full fingerprint', () => {
  const seed = JSON.parse(fs.readFileSync(path.join(__dirname, '../maintainer-signers.json'), 'utf8'));
  const key = seed.maintainers[0].keys[0];
  assert.equal(seed.maintainers[0].id, 5549790); assert.equal(key.primary, '670D249E72D570B836D6F7589272956FD1758DDC');
  assert.equal(sha256(fs.readFileSync(path.join(__dirname, '..', '..', key.path))), key.sha256);
});

test('native: approved signing subkey works; same subkey not listed in B fails', async () => {
  const value = structuredClone(root);
  value.signature = maintainer.sign(value.payload, subkey); rehash(value);
  assert.equal((await check({ commits: [value] })).passed, false);
  const changed = structuredClone(policy); changed.maintainers[0].keys[0].signers.push(subkey);
  assert.equal((await check({ commits: [value], policy: changed })).passed, true);
});
for (const flag of ['BADSIG', 'ERRSIG', 'EXPSIG', 'EXPKEYSIG', 'REVKEYSIG', 'NODATA', 'FAILURE']) {
  test(`status parser rejects ${flag} even alongside VALIDSIG`, async () => {
    const status = `[GNUPG:] NEWSIG\n[GNUPG:] VALIDSIG ${maintainer.primary} 2026-10-22 1792700000 0 4 0 22 8 00 ${maintainer.primary}\n[GNUPG:] ${flag}\n`;
    assert.equal((await check({ commits: [root], status })).passed, false);
  });
}
test('status parser refuses multiple signatures and key-ID-only GOODSIG', async () => {
  assert.equal((await check({ commits: [root], status: '[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG D1758DDC somebody\n' })).passed, false);
  const row = `[GNUPG:] NEWSIG\n[GNUPG:] VALIDSIG ${maintainer.primary} 2026-10-22 1792700000 0 4 0 22 8 00 ${maintainer.primary}\n`;
  assert.equal((await check({ commits: [root], status: row + row })).passed, false);
});
for (const [label, options] of [
  ['empty', { commits: [] }],
  ['duplicates', { commits: [gpg, gpg] }],
  ['missing head', { before: before => { before.head.sha = sha(42); } }],
  ['missing SHA', { page: data => { delete data.commits[0].sha; } }],
  ['malformed SHA', { page: data => { data.commits[0].sha = 'branch'; } }],
  ['missing total', { page: data => { delete data.total_commits; } }],
  ['non-array page', { page: data => { data.commits = {}; } }],
  ['overlong page', { page: data => { data.commits.push({ sha: sha(77) }); } }],
  ['head changed', { after: after => { after.head.sha = sha(42); } }],
  ['head ref changed', { after: after => { after.head.ref = 'other'; } }],
  ['target ref changed', { after: after => { after.base.ref = 'other'; } }],
  ['count changed', { after: after => { after.commits++; } }],
  ['stale event head', { event: event => { event.head.sha = sha(42); } }],
  ['stale event ref', { event: event => { event.head.ref = 'other'; } }],
  ['retargeted event', { event: event => { event.base.ref = 'other'; } }],
  ['missing event head', { event: event => { delete event.head; } }],
  ['missing event base', { event: event => { delete event.base; } }],
  ['nonimmutable base', { before: before => { before.base.sha = 'main'; } }],
  ['nonimmutable head', { before: before => { before.head.sha = 'branch'; } }],
  ['initial API error', { readError: 1 }],
  ['final API error', { readError: 2 }],
  ['full commit API error', { fullError: true }],
]) {
  test(`signature envelope fails closed: ${label}`, async () => assert.equal((await check(options)).passed, false));
}
for (const page of [1, 2, 3]) {
  test(`signature pagination API failure at page ${page}`, async () => assert.equal((await check({ commits: many, pageError: page })).passed, false));
}
test('250-item returned list does not hide metadata count 251', async () => {
  const result = await check({ commits: many, count: 251 });
  assert.equal(result.passed, false); assert.equal(result.calls.pages.length, 0);
});
test('head omission on final comparison page fails', async () => assert.equal((await check({ commits: many.slice(0, 101),
  page: (data, page) => { if (page === 2) data.commits[0].sha = sha(42); } })).passed, false));
test('base policy, never merge-base/head policy, remains the current root', async () => {
  const changed = structuredClone(policy); changed.maintainers[0].keys[0].signers = [subkey];
  assert.equal((await check({ commits: [root], policy: changed, stale: true })).passed, false);
});
test('protected trust-data deletion is not an outsider exception', async () => assert.equal((await checkEarlier({ policyChange: true })).passed, false));

for (const kind of ['gpg', 'ssh']) {
  for (const protectedKind of ['workflows', 'policy']) {
    test(`native ${kind}: transient ${protectedKind} edit/revert rejects outsider despite unchanged HEAD`, async () => {
      const edit = fixture({ kind, state: 1, message: 'Introduce protected edit' });
      const revert = fixture({ kind, state: 0, parents: [edit.sha], message: 'Revert protected edit' });
      assert.equal((await checkEarlier({ commits: [edit, revert], protectedKind })).passed, false);
    });
  }
  test(`native ${kind}: signed merge imports current trusted base workflows`, async () => {
    const value = fixture({ kind, state: 1, merge: true });
    assert.equal((await checkEarlier({ commits: [value], baseState: 1 })).passed, true);
  });
  test(`native ${kind}: exact current state can come from a proven base ancestor`, async () => {
    const value = fixture({ kind, state: 1, parents: [sha(90), sha(91)] });
    assert.equal((await checkEarlier({ commits: [value], baseState: 1, firstParentNew: true })).passed, true);
  });
  test(`native ${kind}: a claimed base parent needs exact immutable ancestry evidence`, async () => {
    const value = fixture({ kind, state: 1, parents: [sha(90), sha(91)] });
    assert.equal((await checkEarlier({ commits: [value], baseState: 1, badProvenance: true })).passed, false);
  });
  test(`native ${kind}: untrusted introduced merge parent cannot launder protected state`, async () => {
    const edit = fixture({ kind, state: 1, message: 'Untrusted workflow' });
    const value = fixture({ kind, state: 1, parents: [sha(90), edit.sha], message: 'Import untrusted workflow' });
    assert.equal((await checkEarlier({ commits: [edit, value], baseState: 1 })).passed, false);
  });
}
test('native: all-maintainer transient edit/revert is authorized under old B', async () => {
  const edit = fixture({ root: true, state: 1, message: 'Maintainer workflow edit' });
  const revert = fixture({ root: true, state: 0, parents: [edit.sha], message: 'Maintainer revert' });
  assert.equal((await check({ commits: [edit, revert] })).passed, true);
});
test('native: dropping policy then reverting cannot bypass whole-PR protection', async () => {
  const edit = fixture({ state: 1, message: 'Delete policy' });
  const revert = fixture({ parents: [edit.sha], message: 'Restore policy' });
  assert.equal((await checkEarlier({ commits: [edit, revert], dropPolicy: true })).passed, false);
});
test('native: old trusted ancestor cannot roll back the current B trust state', async () => {
  const value = fixture({ parents: [sha(91), sha(90)], message: 'Merge older policy' });
  assert.equal((await checkEarlier({ commits: [value], baseState: 1, firstParentNew: true })).passed, false);
});
for (const baseRef of ['release/0.6', 'release/1.20']) {
  for (const [type, value] of [['GPG', gpg], ['SSH', ssh]]) {
    test(`native maintenance ${baseRef}: earlier own ${type} signature uses target B policy`, async () => {
      assert.equal((await checkEarlier({ baseRef, commits: [value] })).passed, true);
    });
    test(`native maintenance ${baseRef}: tampered ${type} signature fails`, async () => {
      const changed = structuredClone(value); changed.payload += 'tamper'; rehash(changed);
      assert.equal((await checkEarlier({ baseRef, commits: [changed] })).passed, false);
    });
  }
  test(`native maintenance ${baseRef}: pinned maintainer GPG is accepted`, async () => {
    assert.equal((await check({ baseRef, commits: [root] })).passed, true);
  });
}
for (const kind of ['gpg', 'ssh']) {
  test(`negative control ${kind}: old net-tree gate accepts edit/revert; final per-commit gate rejects`, async () => {
    const edit = fixture({ kind, state: 1, message: 'Protected negative-control edit' });
    const revert = fixture({ kind, parents: [edit.sha], message: 'Protected negative-control revert' });
    assert.equal((await checkEarlier({ commits: [edit, revert], netOnlyControl: true })).passed, true);
    assert.equal((await checkEarlier({ commits: [edit, revert] })).passed, false);
  });
}

for (const kind of ['gpg', 'ssh']) {
  for (const protectedKind of ['workflows', 'policy']) {
    test(`native ${kind}: merge cannot discard current B ${protectedKind} while retaining first parent`, async () => {
      const value = fixture({ kind, state: 0, parents: [sha(90), B], message: 'Discard current target protected update' });
      const options = { commits: [value], baseState: 1, protectedKind };
      assert.equal((await checkEarlier({ ...options, firstParentOnlyControl: true })).passed, true,
        'Negative control must reproduce v1 first-parent false acceptance');
      assert.equal((await checkEarlier(options)).passed, false);
    });
  }
  test(`native ${kind}: merge unchanged from ALL parents remains an ordinary contribution`, async () => {
    const value = fixture({ kind, state: 0, parents: [sha(90), B] });
    assert.equal((await checkEarlier({ commits: [value] })).passed, true);
  });
  test(`native ${kind}: merge retaining current B as FIRST parent preserves trusted state`, async () => {
    const value = fixture({ kind, state: 1, parents: [B, sha(90)] });
    assert.equal((await checkEarlier({ commits: [value], baseState: 1 })).passed, true);
  });
}
for (const protectedKind of ['workflows', 'policy']) {
  test(`native maintainer: current-base key can authorize explicit ${protectedKind} merge discard`, async () => {
    const value = fixture({ root: true, state: 0, parents: [sha(90), B], message: 'Authorized protected rollback' });
    assert.equal((await check({ commits: [value], baseState: 1, protectedKind })).passed, true);
  });
  test(`native: maintainer head cannot authorize earlier outsider ${protectedKind} merge discard`, async () => {
    const discard = fixture({ state: 0, parents: [sha(90), B], message: 'Unauthorized merge discard' });
    const head = fixture({ root: true, state: 0, parents: [discard.sha], message: 'Maintainer follow-up' });
    assert.equal((await check({ commits: [discard, head], baseState: 1, protectedKind })).passed, false);
  });
}

for (const baseRef of ['main', 'release/0.6']) {
  test(`head ${baseRef}: base-pinned maintainer head passes`, async () => {
    assert.equal((await check({ baseRef, commits: [root] })).passed, true);
  });
  for (const [kind, outside] of [['GPG', gpg], ['SSH', ssh]]) {
    test(`head ${baseRef}: outsider ${kind} alone cannot authorize head`, async () => {
      assert.equal((await check({ baseRef, commits: [outside], noHeadRuleControl: true })).passed, true,
        'Superseded rule must reproduce outsider-only head acceptance');
      const result = await check({ baseRef, commits: [outside] });
      assert.equal(result.passed, false);
      assert.equal(result.calls.registry.length, 0);
    });
    test(`head ${baseRef}: earlier ${kind} plus signed final approval passes`, async () => {
      const final = fixture({ root: true, parents: [outside.sha], message: `Approve ${kind} tree` });
      const result = await check({ baseRef, commits: [outside, final] });
      assert.equal(result.passed, true);
      assert.ok(result.calls.registry.some(route => route.endsWith(kind === 'GPG' ? '/gpg_keys' : '/ssh_signing_keys')));
    });
    test(`head ${baseRef}: ${kind} appended after signed approval fails`, async () => {
      const final = fixture({ kind: kind.toLowerCase(), parents: [root.sha], message: 'After approval' });
      assert.equal((await check({ baseRef, commits: [root, final] })).passed, false);
    });
    test(`head ${baseRef}: final approval cannot launder protected changes`, async () => {
      assert.equal((await check({ baseRef, commits: [outside, rootMerge], protected: true })).passed, false);
    });
  }
}
test('head: maintainer key authorizes head independently of linked account labels', async () => {
  const final = structuredClone(root); final.authorActor = gpg.authorActor; final.actor = gpg.actor;
  assert.equal((await check({ commits: [final] })).passed, true);
});
test('head: maintainer labels cannot authorize an unknown key', async () => {
  const final = structuredClone(gpg); final.authorActor = root.authorActor; final.actor = root.actor;
  assert.equal((await check({ commits: [final] })).passed, false);
});
test('head: web-flow identity remains denied', async () => {
  assert.equal((await check({ commits: [root], full: c => { c.committer = { id: 19864447, login: 'web-flow' }; } })).passed, false);
});
test('head: tampered head cannot reuse signature', async () => {
  const final = structuredClone(root); final.payload += 'tamper'; rehash(final);
  assert.equal((await check({ commits: [final] })).passed, false);
});
test('head: unverified head fails', async () => {
  assert.equal((await check({ commits: [root], raw: c => { c.verification.verified = false; } })).passed, false);
});
test('head: missing base policy cannot fall back to head', async () => {
  assert.equal((await check({ commits: [root], missingBase: true })).passed, false);
});
test('head: key not authorized by current base fails', async () => {
  const changed = structuredClone(policy); changed.maintainers[0].keys[0].signers = [subkey];
  assert.equal((await check({ commits: [root], policy: changed })).passed, false);
});
test('head: explicitly allowed signing subkey approves head', async () => {
  const final = structuredClone(root); final.signature = maintainer.sign(final.payload, subkey); rehash(final);
  const changed = structuredClone(policy); changed.maintainers[0].keys[0].signers.push(subkey);
  assert.equal((await check({ commits: [final], policy: changed })).passed, true);
});
test('head: changed head is detected by final API reread', async () => {
  assert.equal((await check({ commits: [root], after: p => { p.head.sha = sha(42); } })).passed, false);
});
test('head: moved base is detected by final API reread', async () => {
  assert.equal((await check({ commits: [root], after: p => { p.base.sha = sha(42); } })).passed, false);
});
test('head: missing head in comparison fails', async () => {
  assert.equal((await check({ commits: [root], before: p => { p.head.sha = sha(42); } })).passed, false);
});
test('head: actual SHA rather than comparison position selects head', async () => {
  assert.equal((await check({ commits: [root, gpg], before: p => { p.head.sha = root.sha; } })).passed, true);
});
test('head: mixed contributor signing kinds retain a real signed parent chain', async () => {
  const second = fixture({ kind: 'ssh', parents: [gpg.sha], message: 'Second contribution' });
  const final = fixture({ root: true, parents: [second.sha], message: 'Approve mixed chain' });
  assert.equal((await check({ commits: [gpg, second, final] })).passed, true);
});
test('head: valid maintainer head cannot override a bad earlier contributor signature', async () => {
  const bad = structuredClone(gpg); bad.payload += 'tamper'; rehash(bad);
  const final = fixture({ root: true, parents: [bad.sha], message: 'Final approval' });
  assert.equal((await check({ commits: [bad, final] })).passed, false);
});
