// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
// Run with Node.js, Ruby (YAML parser), and Git: node --test .github/tests/dco.test.cjs
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

function readWorkflow(name) {
  const file = path.join(__dirname, `../workflows/${name}.yml`);
  return JSON.parse(execFileSync('ruby', ['-ryaml', '-rjson', '-e',
    'puts JSON.generate(YAML.safe_load(File.read(ARGV[0])))', file], { encoding: 'utf8' }));
}
const workflows = { dco: readWorkflow('dco'), 'commit-signatures': readWorkflow('commit-signatures') };
const testsWorkflow = readWorkflow('dco-tests');
const checks = Object.fromEntries(Object.entries(workflows).map(([name, workflow]) => {
  const script = workflow.jobs[name].steps[0].with.script;
  return [name, vm.runInNewContext(`(async function(github, context, core, require) {${script}\n})`)];
}));
const eventTypes = ['opened', 'synchronize', 'reopened', 'ready_for_review', 'edited'];
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
    base: { sha: sha(99), ref: 'main' }, commits: 'count' in options ? options.count : commits.length,
  };
  const after = structuredClone(before);
  options.changeAfter?.(after);
  const context = { repo: { owner: 'fixture', repo: 'project' },
    sha: options.contextSHA ?? before.base.sha, ref: 'refs/heads/main', payload: {
    pull_request: { number: 7, head: { sha: options.eventHead ?? before.head.sha,
      ref: options.eventHeadRef ?? before.head.ref, repo: { owner: { login: 'contributor' }, name: 'fork' } },
      base: { ref: options.eventBase ?? 'main' } },
  } };
  options.changeEvent?.(context.payload.pull_request);
  const errors = [], failures = [], info = [];
  let reads = 0, signatures = 0, comparisons = 0, parsers = 0;
  const listCommits = Symbol('listCommits');
  const github = {
    rest: {
      pulls: { listCommits, get: async params => {
        assert.deepEqual({ ...params }, { ...context.repo, pull_number: 7 });
        if (++reads === options.errorAt) throw new Error('API unavailable');
        return { data: reads === 1 ? before : after };
      } },
      repos: { compareCommits: async params => {
        comparisons++;
        assert.deepEqual({ ...params }, { ...context.repo, base: sha(90), head: before.base.sha });
        if (options.ancestryError) throw new Error('Ancestry unavailable');
        return { data: { status: options.ancestry ?? 'ahead' } };
      } },
    },
    paginate: async (method, params) => {
      assert.equal(method, listCommits);
      assert.deepEqual({ ...params }, { ...context.repo, pull_number: 7, per_page: 100 });
      if (options.pageError) throw new Error('Pagination unavailable');
      return commits;
    },
    graphql: async (query, params) => {
      signatures++;
      assert.ok(query.includes('wasSignedByGitHub'));
      assert.deepEqual({ ...params }, { ...context.repo, oid: sha(2) });
      if (options.signatureError) throw new Error('Signature unavailable');
      return { repository: { object: { signature: options.signature === undefined
        ? { isValid: true, wasSignedByGitHub: true, signer: { login: 'web-flow' } }
        : options.signature } } };
    },
  };
  // The only subprocess is a fixed Git trailer parser; PR text is data on stdin.
  const parserOnlyRequire = name => {
    assert.equal(name, 'node:child_process');
    return { execFileSync: (command, args, settings) => {
      parsers++;
      assert.equal(command, 'git');
      assert.deepEqual([...args], ['-c', 'trailer.separators=:',
        'interpret-trailers', '--parse', '--no-divider']);
      assert.deepEqual(Object.keys(settings).sort(), ['encoding', 'input']);
      assert.equal(settings.encoding, 'utf8');
      assert.equal(typeof settings.input, 'string');
      return execFileSync(command, [...args], { ...settings });
    } };
  };
  let thrown;
  try {
    await checks[options.kind ?? 'dco'](github, context, {
      error: value => errors.push(value), setFailed: value => failures.push(value),
      info: value => info.push(value),
    }, parserOnlyRequire);
  } catch (error) { thrown = error; }
  return { passed: !thrown && failures.length === 0, errors, failures, info,
    reads, signatures, comparisons, parsers, thrown };
}

for (const [name, workflow] of Object.entries(workflows)) {
  test(`${name}: trusted API-only workflow cannot check out or run PR code`, () => {
    assert.equal(workflow.name, name);
    assert.deepEqual(Object.keys(workflow).sort(), ['jobs', 'name', 'permissions',
      workflow.on ? 'on' : 'true'].sort());
    assert.deepEqual(workflow.permissions, { contents: 'read', 'pull-requests': 'read' });
    assert.deepEqual(workflow.on ?? workflow.true, { pull_request_target: { types: eventTypes } });
    assert.deepEqual(Object.keys(workflow.jobs), [name]);
    const job = workflow.jobs[name];
    assert.deepEqual(Object.keys(job).sort(), ['runs-on', 'steps', 'timeout-minutes']);
    assert.equal(job['runs-on'], 'ubuntu-24.04');
    assert.equal(job['timeout-minutes'], 5);
    assert.equal(job.steps.length, 1);
    const step = job.steps[0];
    assert.deepEqual(Object.keys(step).sort(), ['name', 'uses', 'with']);
    assert.equal(step.uses, 'actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3');
    assert.deepEqual(Object.keys(step.with), ['script']);
    const script = step.with.script;
    assert.ok(!script.includes('${{'));
    assert.doesNotMatch(script, /\b(?:eval|Function|import)\s*\(/);
    assert.deepEqual([...script.matchAll(/\brequire\(([^)]+)\)/g)].map(match => match[1]),
      name === 'dco' ? ["'node:child_process'"] : []);
  });
}

test('PR-controlled test execution stays in an unprivileged pull_request workflow', () => {
  assert.equal(testsWorkflow.name, 'dco-tests');
  assert.deepEqual(testsWorkflow.on ?? testsWorkflow.true, { pull_request: { types: eventTypes } });
  assert.deepEqual(testsWorkflow.permissions, { contents: 'read' });
  assert.deepEqual(Object.keys(testsWorkflow.jobs), ['dco-tests']);
  const job = testsWorkflow.jobs['dco-tests'];
  assert.deepEqual(Object.keys(job).sort(), ['runs-on', 'steps', 'timeout-minutes']);
  assert.equal(job.steps.length, 2);
  assert.deepEqual(job.steps[0], {
    uses: 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
    with: { 'persist-credentials': false },
  });
  assert.deepEqual(job.steps[1], {
    name: 'Test PR commit checks and workflow boundaries', run: 'node --test .github/tests/dco.test.cjs',
  });
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
  ['missing commit SHA', { commits: [{ ...commit(1), sha: undefined }, commit(2)] }],
  ['malformed commit SHA', { commits: [{ ...commit(1), sha: 'not-a-commit' }, commit(2)] }],
  ['missing head', { head: sha(2) }],
  ['invalid count', { count: '1' }],
  ['missing count', { count: undefined }],
  ['null count', { count: null }],
  ['zero count', { count: 0 }],
  ['negative count', { count: -1 }],
  ['fractional count', { count: 1.5 }],
  ['stale head', { eventHead: sha(2) }],
  ['stale head ref', { eventHeadRef: 'other' }],
  ['missing event head', { changeEvent: event => { delete event.head; } }],
  ['missing event base', { changeEvent: event => { delete event.base; } }],
  ['retargeted event', { eventBase: 'other' }],
  ['head race', { changeAfter: c => { c.head.sha = sha(2); } }],
  ['head ref race', { changeAfter: c => { c.head.ref = 'other'; } }],
  ['base race', { changeAfter: c => { c.base.sha = sha(98); } }],
  ['base ref race', { changeAfter: c => { c.base.ref = 'other'; } }],
  ['count race', { changeAfter: c => { c.commits = 2; } }],
  ['initial API failure', { errorAt: 1 }],
  ['final API failure', { errorAt: 2 }],
  ['pagination failure', { pageError: true }],
]) {
  for (const kind of Object.keys(checks)) {
    test(`${kind}: fail closed on ${name}`, async () => {
      const result = await check({ ...options, kind });
      assert.equal(result.passed, false);
      assert.ok(result.thrown);
      assert.ok(!result.info.some(line => line.startsWith('All ')));
    });
  }
}

for (const error of ['signatureError', 'ancestryError']) {
  test(`dco: fail closed on branch-update ${error}`, async () => {
    const result = await check({ commits: [commit(1), update()], [error]: true });
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
for (const kind of Object.keys(checks)) {
  test(`${kind}: event PR head is checked, never the base workflow SHA or fork repository`, async () => {
    const result = await check({ kind, contextSHA: sha(500) });
    assert.equal(result.passed, true);
    assert.equal(result.reads, 2);
    assert.equal(result.parsers, kind === 'dco' ? 1 : 0);
  });
  test(`${kind}: PR title and body are data, never checker code`, async () => {
    const result = await check({ kind, changeEvent: event => {
      event.title = '${{ secrets.GITHUB_TOKEN }}';
      event.body = 'throw new Error("PR-controlled code executed"); $(exit 91)';
    } });
    assert.equal(result.passed, true);
  });
  test(`${kind}: 250 valid commits pass`, async () => {
    const result = await check({ kind, commits: Array.from({ length: 250 }, (_, i) => commit(i + 1)) });
    assert.equal(result.passed, true);
  });
  test(`${kind}: API cap never approves an incomplete list`, async () => {
    const result = await check({ kind, commits: Array.from({ length: 250 }, (_, i) => commit(i + 1)), count: 251 });
    assert.equal(result.passed, false);
    assert.ok(result.thrown);
  });
  test(`${kind}: reject over-limit PR even if an API returns all 251 commits`, async () => {
    const result = await check({ kind, commits: Array.from({ length: 251 }, (_, i) => commit(i + 1)) });
    assert.equal(result.passed, false);
    assert.ok(result.thrown);
  });
}

test('signature check reports every unverified commit, not just the latest', async () => {
  const commits = [commit(1), commit(2), commit(3)];
  commits[0].commit.verification = { verified: false, reason: 'unsigned' };
  delete commits[1].commit.verification;
  const result = await check({ kind: 'commit-signatures', commits });
  assert.equal(result.passed, false);
  assert.equal(result.errors.length, 2);
  assert.ok(result.errors[0].includes(sha(1)));
  assert.ok(result.errors[0].includes('unsigned'));
  assert.ok(result.errors[1].includes(sha(2)));
  assert.ok(result.errors[1].includes('verification unavailable'));
  assert.equal(result.failures.length, 1);
  assert.ok(result.failures[0].startsWith('2 of 3'));
});

for (const verification of [undefined, null, {}, { verified: false }, { verified: 'true' }, { verified: 1 }]) {
  test(`signature check requires boolean verified=true: ${JSON.stringify(verification)}`, async () => {
    const value = commit(1);
    value.commit.verification = verification;
    assert.equal((await check({ kind: 'commit-signatures', commits: [value] })).passed, false);
  });
}

test('signature check rejects a missing commit payload', async () => {
  const value = commit(1);
  delete value.commit;
  assert.equal((await check({ kind: 'commit-signatures', commits: [value] })).passed, false);
});

test('signature check does not exempt unsigned bots or branch-update merges', async () => {
  const bot = commit(1);
  bot.author = { type: 'Bot' };
  const merge = update();
  for (const value of [bot, merge]) value.commit.verification.verified = false;
  const result = await check({ kind: 'commit-signatures', commits: [bot, merge] });
  assert.equal(result.passed, false);
  assert.equal(result.errors.length, 2);
  assert.equal(result.signatures, 0);
  assert.equal(result.comparisons, 0);
  assert.equal(result.parsers, 0);
});

test('signatures and DCO remain separate requirements', async () => {
  const signedOnly = commit(1, 'No author sign-off');
  assert.equal((await check({ kind: 'commit-signatures', commits: [signedOnly] })).passed, true);
  assert.equal((await check({ commits: [signedOnly] })).passed, false);
  const signoffOnly = commit(1);
  signoffOnly.commit.verification.verified = false;
  assert.equal((await check({ kind: 'commit-signatures', commits: [signoffOnly] })).passed, false);
  assert.equal((await check({ commits: [signoffOnly] })).passed, true);
});

test('unverified signature on the third page is reported', async () => {
  const commits = Array.from({ length: 201 }, (_, i) => commit(i + 1));
  commits[200].commit.verification.verified = false;
  const result = await check({ kind: 'commit-signatures', commits });
  assert.equal(result.passed, false);
  assert.ok(result.errors[0].includes(sha(201)));
});
