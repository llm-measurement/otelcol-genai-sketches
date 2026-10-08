// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
// Run with Node.js, Ruby (YAML parser), and Git: node --test .github/tests/dco.test.cjs
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const file = path.join(__dirname, '../workflows/dco.yml');
const workflow = JSON.parse(execFileSync('ruby', ['-ryaml', '-rjson', '-e',
  'puts JSON.generate(YAML.safe_load(File.read(ARGV[0])))', file], { encoding: 'utf8' }));
const script = workflow.jobs.dco.steps[0].with.script;
const run = vm.runInNewContext(`(async function(github, context, core, require) {${script}\n})`);
const sha = n => n.toString(16).padStart(40, '0');
const trailer = 'Signed-off-by: Example Author <author@example.test>';
const commit = (n, message = `Change\n\n${trailer}`) => ({
  sha: sha(n), parents: [{ sha: sha(90) }],
  commit: { message, author: { name: 'Example Author', email: 'author@example.test' },
    committer: { name: 'Example Author', email: 'author@example.test' },
    verification: { verified: true } },
  committer: { login: 'example' },
});
const update = () => {
  const value = commit(2, "Merge branch 'main' into contribution");
  value.parents = [{ sha: sha(1) }, { sha: sha(90) }];
  value.commit.committer = { name: 'GitHub', email: 'noreply@github.com' };
  value.committer.login = 'web-flow';
  return value;
};

async function check(options = {}) {
  const commits = options.commits ?? [commit(1)];
  const before = {
    head: { sha: options.head ?? commits.at(-1)?.sha ?? sha(1), ref: 'contribution' },
    base: { sha: sha(99), ref: 'main' }, commits: options.count ?? commits.length,
  };
  const after = structuredClone(before);
  options.changeAfter?.(after);
  const context = { repo: { owner: 'fixture', repo: 'project' }, payload: {
    pull_request: { number: 7, head: { sha: options.eventHead ?? before.head.sha },
      base: { ref: options.eventBase ?? 'main' } },
  } };
  const errors = [], failures = [], info = [];
  let reads = 0, signatures = 0, comparisons = 0;
  const listCommits = Symbol('listCommits');
  const github = {
    rest: {
      pulls: { listCommits, get: async params => {
        assert.equal(params.pull_number, 7);
        if (++reads === options.errorAt) throw new Error('API unavailable');
        return { data: reads === 1 ? before : after };
      } },
      repos: { compareCommits: async params => {
        comparisons++;
        assert.equal(params.base, sha(90));
        assert.equal(params.head, before.base.sha);
        if (options.ancestryError) throw new Error('Ancestry unavailable');
        return { data: { status: options.ancestry ?? 'ahead' } };
      } },
    },
    paginate: async (method, params) => {
      assert.equal(method, listCommits);
      assert.equal(params.per_page, 100);
      if (options.pageError) throw new Error('Pagination unavailable');
      return commits;
    },
    graphql: async (query, params) => {
      signatures++;
      assert.ok(query.includes('wasSignedByGitHub'));
      assert.equal(params.oid, sha(2));
      if (options.signatureError) throw new Error('Signature unavailable');
      return { repository: { object: { signature: options.signature === undefined
        ? { isValid: true, wasSignedByGitHub: true, signer: { login: 'web-flow' } }
        : options.signature } } };
    },
  };
  let thrown;
  try {
    await run(github, context, {
      error: value => errors.push(value), setFailed: value => failures.push(value),
      info: value => info.push(value),
    }, require);
  } catch (error) { thrown = error; }
  return { passed: !thrown && failures.length === 0, errors, failures, info,
    signatures, comparisons, thrown };
}

test('read-only check uses no checkout, shell interpolation, or bypass', () => {
  assert.deepEqual(workflow.permissions, { contents: 'read', 'pull-requests': 'read' });
  assert.deepEqual(Object.keys(workflow.on ?? workflow.true), ['pull_request']);
  assert.deepEqual((workflow.on ?? workflow.true).pull_request.types,
    ['opened', 'synchronize', 'reopened', 'ready_for_review', 'edited']);
  assert.equal(workflow.jobs.dco.if, undefined);
  assert.equal(workflow.jobs.dco.steps.length, 1);
  assert.equal(workflow.jobs.dco.steps[0].if, undefined);
  assert.match(workflow.jobs.dco.steps[0].uses, /^actions\/github-script@[a-f0-9]{40}$/);
  assert.ok(!script.includes('${{'));
  assert.deepEqual(workflow.jobs['dco-tests'].permissions, { contents: 'read' });
  assert.equal(workflow.jobs['dco-tests'].steps[0].with['persist-credentials'], false);
});

const messages = [
  ['matching trailer', `Change\n\n${trailer}`, true],
  ['extra trailers', `Change\n\nReviewed-by: Someone <someone@example.test>\n${trailer}`, true],
  ['multiple sign-offs', `Change\n\nSigned-off-by: Other <other@example.test>\n${trailer}`, true],
  ['body divider before final trailers', `Change\n\n---\nDependency details.\n\n${trailer}`, true],
  ['CRLF', `Change\r\n\r\n${trailer}\r\n`, true],
  ['trailing newline', `Change\n\n${trailer}\n\n`, true],
  ['missing sign-off', 'Change', false],
  ['signature is not a sign-off', 'Change\n\nSigned: true', false],
  ['subject only', trailer, false],
  ['body rather than trailer', `Change\n\n${trailer}\n\nMore explanation.`, false],
  ['quoted text', `Change\n\n> ${trailer}`, false],
  ['wrong name', 'Change\n\nSigned-off-by: Other <author@example.test>', false],
  ['wrong email', 'Change\n\nSigned-off-by: Example Author <other@example.test>', false],
  ['suffix mismatch', `Change\n\n${trailer} (for someone else)`, false],
];
for (const [name, message, passed] of messages) {
  test(name, async () => assert.equal((await check({ commits: [commit(1, message)] })).passed, passed));
}

test('every failure names its commit and gives signed repair commands', async () => {
  const result = await check({ commits: [commit(1, 'No trailer'), commit(2, 'None')] });
  assert.equal(result.passed, false);
  assert.equal(result.errors.length, 2);
  for (const [i, error] of result.errors.entries()) {
    assert.ok(error.includes(sha(i + 1)));
    assert.ok(error.includes('git commit --amend -s -S'));
    assert.ok(error.includes('git rebase --signoff --gpg-sign origin/main'));
    assert.ok(error.includes('git push --force-with-lease'));
  }
});

for (const [name, mutate] of [
  ['bot without sign-off', c => { c.author = { type: 'Bot' }; }],
  ['missing author', c => { delete c.commit.author; }],
  ['missing email', c => { delete c.commit.author.email; }],
  ['missing name', c => { delete c.commit.author.name; }],
  ['missing message', c => { delete c.commit.message; }],
]) {
  test(name, async () => {
    const value = commit(1, 'Missing');
    mutate(value);
    assert.equal((await check({ commits: [value] })).passed, false);
  });
}

test('commit data is never shell code', async () => {
  const value = commit(1);
  value.commit.author.name = 'Example $(exit 91)';
  value.commit.message = 'Change\n\nSigned-off-by: Example $(exit 91) <author@example.test>';
  assert.equal((await check({ commits: [value] })).passed, true);
});

test('Dependabot sign-off must match its actual author email too', async () => {
  const value = commit(1, 'Update dependency\n\nSigned-off-by: dependabot[bot] <support@github.com>');
  value.commit.author = { name: 'dependabot[bot]', email: '49699333+dependabot[bot]@users.noreply.github.com' };
  assert.equal((await check({ commits: [value] })).passed, false);
  value.commit.message = `Update dependency\n\nSigned-off-by: ${value.commit.author.name} <${value.commit.author.email}>`;
  assert.equal((await check({ commits: [value] })).passed, true);
});

for (const ancestry of ['ahead', 'identical']) {
  test(`GitHub branch-update exemption with ${ancestry} base`, async () => {
    const result = await check({ commits: [commit(1), update()], ancestry });
    assert.equal(result.passed, true);
    assert.equal(result.signatures, 1);
    assert.equal(result.comparisons, 1);
    assert.ok(result.info.some(line => line.includes(`Exempt GitHub branch-update merge: ${sha(2)}`)));
  });
}

for (const [name, mutate] of [
  ['message-only forgery', c => { c.committer.login = 'example'; }],
  ['one-parent commit', c => { c.parents.pop(); }],
  ['octopus merge', c => { c.parents.push({ sha: sha(89) }); }],
  ['parent outside PR', c => { c.parents[0].sha = sha(88); }],
  ['unsigned merge', c => { c.commit.verification.verified = false; }],
  ['spoofed committer name', c => { c.commit.committer.name = 'Other'; }],
  ['spoofed committer email', c => { c.commit.committer.email = 'other@example.test'; }],
  ['wrong source branch', c => { c.commit.message = "Merge branch 'other' into contribution"; }],
  ['wrong target branch', c => { c.commit.message = "Merge branch 'main' into other"; }],
  ['merge with extra message', c => { c.commit.message += '\n\nManual edits'; }],
]) {
  test(name, async () => {
    const value = update();
    mutate(value);
    const result = await check({ commits: [commit(1), value] });
    assert.equal(result.passed, false);
    assert.equal(result.signatures, 0);
  });
}

for (const signature of [null, {}, { isValid: false, wasSignedByGitHub: true },
  { isValid: true, wasSignedByGitHub: false, signer: { login: 'web-flow' } },
  { isValid: true, wasSignedByGitHub: true, signer: { login: 'other' } }]) {
  test(`reject untrusted merge signature ${JSON.stringify(signature)}`, async () => {
    assert.equal((await check({ commits: [commit(1), update()], signature })).passed, false);
  });
}
for (const ancestry of ['behind', 'diverged', 'unknown']) {
  test(`reject merge from non-base history: ${ancestry}`, async () => {
    assert.equal((await check({ commits: [commit(1), update()], ancestry })).passed, false);
  });
}
test('branch-update exemption never covers the earlier contribution', async () => {
  const result = await check({ commits: [commit(1, 'No sign-off'), update()] });
  assert.equal(result.passed, false);
  assert.equal(result.errors.length, 1);
  assert.ok(result.errors[0].includes(sha(1)));
});

for (const [name, options] of [
  ['empty list', { commits: [] }],
  ['partial list', { count: 2 }],
  ['duplicate commits', { commits: [commit(1), commit(1)] }],
  ['missing head', { head: sha(2) }],
  ['invalid count', { count: '1' }],
  ['stale head', { eventHead: sha(2) }],
  ['retargeted event', { eventBase: 'other' }],
  ['head race', { changeAfter: c => { c.head.sha = sha(2); } }],
  ['head ref race', { changeAfter: c => { c.head.ref = 'other'; } }],
  ['base race', { changeAfter: c => { c.base.sha = sha(98); } }],
  ['base ref race', { changeAfter: c => { c.base.ref = 'other'; } }],
  ['count race', { changeAfter: c => { c.commits = 2; } }],
  ['initial API failure', { errorAt: 1 }],
  ['final API failure', { errorAt: 2 }],
  ['pagination failure', { pageError: true }],
  ['signature API failure', { commits: [commit(1), update()], signatureError: true }],
  ['ancestry API failure', { commits: [commit(1), update()], ancestryError: true }],
]) {
  test(`fail closed on ${name}`, async () => {
    const result = await check(options);
    assert.equal(result.passed, false);
    assert.ok(result.thrown);
  });
}

test('missing sign-off on the third page is reported', async () => {
  const commits = Array.from({ length: 201 }, (_, i) => commit(i + 1));
  commits[200].commit.message = 'No sign-off';
  const result = await check({ commits });
  assert.equal(result.passed, false);
  assert.ok(result.errors[0].includes(sha(201)));
});
test('250 signed-off commits pass', async () => {
  assert.equal((await check({ commits: Array.from({ length: 250 }, (_, i) => commit(i + 1)) })).passed, true);
});
test('API cap never approves an incomplete list', async () => {
  assert.equal((await check({ commits: Array.from({ length: 250 }, (_, i) => commit(i + 1)), count: 251 })).passed, false);
});
