// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

function workflow(name) {
  return JSON.parse(execFileSync('ruby', ['-ryaml', '-rjson', '-e',
    'puts JSON.generate(YAML.safe_load(File.read(ARGV[0])))',
    path.join(__dirname, `../workflows/${name}.yml`)], { encoding: 'utf8' }));
}
const release = workflow('release');
const step = release.jobs.verify.steps[1];
const run = vm.runInNewContext(`(async function(github, context, core) {${step.with.script}\n})`);
const sha = digit => digit.repeat(40);
const repository = 'llm-measurement/otelcol-genai-sketches';
const checks = ['test', 'example-smoke', 'commit-signatures-trusted', 'dco-trusted', 'trusted-api-tests'];
const policy = () => [
  ...['deletion', 'non_fast_forward', 'required_linear_history', 'required_signatures'].map(type => ({ type })),
  { type: 'pull_request', parameters: { required_approving_review_count: 0,
    required_review_thread_resolution: true, allowed_merge_methods: ['squash', 'rebase'] } },
  { type: 'required_status_checks', parameters: { strict_required_status_checks_policy: true,
    do_not_enforce_on_create: false, required_status_checks: checks.map(context => ({ context, integration_id: 15368 })) } },
];

async function verify(options = {}) {
  const context = { repo: { owner: 'llm-measurement', repo: 'otelcol-genai-sketches' },
    sha: sha('a'), ref: 'refs/tags/v0.6.2', eventName: 'push', ...options.context };
  const mainPolicy = { id: 20402161, name: 'Protect main', target: 'branch', enforcement: 'active',
    source_type: 'Repository', source: repository, rules: policy() };
  const maintenance = { ...structuredClone(mainPolicy), id: 123, name: 'Protect maintenance releases',
    conditions: { ref_name: { include: ['refs/heads/release/*'], exclude: [] } } };
  options.mainPolicy?.(mainPolicy);
  options.maintenance?.(maintenance);
  const mainRules = mainPolicy.rules.map(rule => ({ ...structuredClone(rule),
    ruleset_id: mainPolicy.id, ruleset_source_type: 'Repository', ruleset_source: repository }));
  const branchRules = maintenance.rules.map(rule => ({ ...structuredClone(rule),
    ruleset_id: maintenance.id, ruleset_source_type: 'Repository', ruleset_source: repository }));
  options.mainRules?.(mainRules);
  options.branchRules?.(branchRules);
  let branchReads = 0;
  const policyReads = new Map(), effectiveReads = new Map();
  const calls = [], info = [];
  const github = {
    rest: { repos: {
      getBranch: async params => {
        calls.push(['branch', params.branch]);
        if (options.apiError === 'branch') throw new Error('API unavailable');
        assert.equal(params.owner, 'llm-measurement');
        assert.equal(params.repo, 'otelcol-genai-sketches');
        if (params.branch === 'main') return { data: { protected: options.mainProtected ?? true, commit: { sha: sha('b') } } };
        assert.equal(params.branch, options.expectedBranch ?? 'release/0.6');
        if (options.missingBranch) throw new Error('404');
        branchReads++;
        if (branchReads === 2) options.onCompletion?.({ mainPolicy, maintenance, mainRules, branchRules });
        return { data: { protected: branchReads > 1 && options.unprotectRace ? false : options.protected ?? true,
          commit: { sha: branchReads > 1 && options.branchRace ? sha('d') : options.branchSHA ?? sha('c') } } };
      },
      compareCommits: async params => {
        calls.push(['compare', params.base, params.head]);
        assert.equal(params.base, context.sha);
        assert.match(params.head, /^[a-f0-9]{40}$/);
        if (options.apiError === 'compare') throw new Error('API unavailable');
        return { data: { base_commit: { sha: options.wrongBase ? sha('e') : context.sha },
          status: params.head === sha('b') ? options.mainStatus ?? 'diverged' : options.branchStatus ?? 'ahead' } };
      },
    } },
    paginate: async (route, params) => {
      calls.push(['rules', params.branch]);
      assert.equal(route, 'GET /repos/{owner}/{repo}/rules/branches/{branch}');
      assert.equal(params.per_page, 100);
      if (options.apiError === 'rules') throw new Error('API unavailable');
      effectiveReads.set(params.branch, (effectiveReads.get(params.branch) ?? 0) + 1);
      const name = params.branch === 'main' ? 'main-rules' : 'maintenance-rules';
      if (effectiveReads.get(params.branch) > 1 && options.lateAPIError === name) throw new Error('Late API unavailable');
      return structuredClone(params.branch === 'main' ? mainRules : branchRules);
    },
    request: async (route, params) => {
      calls.push(['ruleset', params.ruleset_id]);
      assert.equal(route, 'GET /repos/{owner}/{repo}/rulesets/{ruleset_id}');
      if (options.apiError === 'ruleset') throw new Error('API unavailable');
      assert.ok([20402161, 123].includes(params.ruleset_id));
      policyReads.set(params.ruleset_id, (policyReads.get(params.ruleset_id) ?? 0) + 1);
      const name = params.ruleset_id === 20402161 ? 'baseline' : 'maintenance-ruleset';
      if (policyReads.get(params.ruleset_id) > 1 && options.lateAPIError === name) throw new Error('Late API unavailable');
      return { data: structuredClone(params.ruleset_id === 20402161 ? mainPolicy : maintenance) };
    },
  };
  let error;
  try { await run(github, context, { info: value => info.push(value) }); }
  catch (caught) { error = caught; }
  return { passed: !error, error, calls, info, branchReads };
}

test('source gate is pinned, read-only and precedes all build/publication work', () => {
  assert.equal(step.name, 'Require main or the protected matching maintenance line');
  assert.equal(step.uses, 'actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3');
  assert.deepEqual(release.permissions, { contents: 'read' });
  assert.equal(release.jobs.verify.permissions, undefined);
  assert.equal(step.if, undefined);
  assert.equal(step['continue-on-error'], undefined);
  assert.doesNotMatch(step.with.script, /\$\{\{|\brequire\(|\bfetch\(|\b(?:POST|PUT|PATCH|DELETE)\b/);
  assert.deepEqual(release.jobs.image.needs, ['verify']);
  assert.ok(release.jobs.publish.needs.includes('verify'));
});
test('all five main-required checks are available on maintenance PRs', () => {
  const ci = workflow('ci');
  assert.ok(Object.hasOwn(ci.on ?? ci.true, 'pull_request'));
  assert.equal((ci.on ?? ci.true).pull_request?.branches, undefined);
  assert.ok(ci.jobs.test && ci.jobs['example-smoke']);
  for (const name of ['commit-signatures-trusted', 'dco-trusted', 'trusted-api-tests']) {
    const value = workflow(name);
    const event = name === 'trusted-api-tests' ? 'pull_request' : 'pull_request_target';
    assert.ok(Object.hasOwn(value.on ?? value.true, event));
    assert.equal((value.on ?? value.true)[event].branches, undefined);
    assert.equal((value.on ?? value.true)[event]['branches-ignore'], undefined);
    assert.ok(value.jobs[name]);
  }
  assert.equal(workflow('trusted-api-tests').jobs['trusted-api-tests'].steps[1].run,
    'node --test .github/tests/*.test.cjs');
});
for (const status of ['ahead', 'identical']) {
  test(`main ancestor remains supported: ${status}`, async () => {
    const result = await verify({ mainStatus: status });
    assert.equal(result.passed, true, result.error);
    assert.equal(result.calls.some(call => call[0] === 'rules'), false);
    assert.equal(result.calls.some(call => call[0] === 'ruleset'), false);
  });
  test(`protected exact maintenance line accepted: ${status}`, async () => {
    assert.equal((await verify({ branchStatus: status })).passed, true);
  });
}
test('existing prerelease syntax maps to the same exact minor line', async () => {
  assert.equal((await verify({ context: { ref: 'refs/tags/v1.20.3-rc.1' }, expectedBranch: 'release/1.20' })).passed, true);
});
test('explicit empty bypass list is accepted', async () => {
  assert.equal((await verify({ maintenance: value => { value.bypass_actors = []; } })).passed, true);
});
test('redacted bypass actors require separate administrative review, never a no-bypass claim', async () => {
  const result = await verify();
  assert.equal(result.passed, true);
  assert.match(result.info[0], /administrative no-bypass review is also required/);
});
const cases = [
  ['unprotected branch', { protected: false }],
  ['unprotected main', { mainProtected: false }],
  ['missing maintenance line', { missingBranch: true }],
  ['wrong minor branch ancestry', { branchStatus: 'diverged' }],
  ['tag ahead of branch', { branchStatus: 'behind' }],
  ['unknown ancestry', { branchStatus: 'unknown' }],
  ['unbound API comparison', { wrongBase: true }],
  ['branch SHA injection', { branchSHA: 'release/0.6; exit 0' }],
  ['branch moved', { branchRace: true }],
  ['protection removed', { unprotectRace: true }],
  ['wrong repository', { context: { repo: { owner: 'fork', repo: 'project' } } }],
  ['wrong event', { context: { eventName: 'workflow_dispatch' } }],
  ['invalid source SHA', { context: { sha: 'refs/heads/main' } }],
  ...['refs/heads/release/0.6', 'refs/tags/chart-v0.6.2', 'refs/tags/v00.6.2',
    'refs/tags/v0.06.2', 'refs/tags/v0.6.02', 'refs/tags/v0.6', 'refs/tags/v0.6.2/path',
    'refs/tags/v0.6.2+metadata', 'refs/tags/v0.6.2\n'].map(ref => ['invalid tag ' + JSON.stringify(ref), { context: { ref } }]),
  ['disabled ruleset', { maintenance: value => { value.enforcement = 'disabled'; } }],
  ['evaluate-only ruleset', { maintenance: value => { value.enforcement = 'evaluate'; } }],
  ['wrong ruleset source', { maintenance: value => { value.source = 'fork/project'; } }],
  ['wrong ruleset name', { maintenance: value => { value.name = 'unreviewed'; } }],
  ['branch-name-only protection', { branchRules: value => { value.length = 0; } }],
  ['excluded release branch', { maintenance: value => { value.conditions.ref_name.exclude = ['refs/heads/release/0.6']; } }],
  ['nonmatching approved pattern', { maintenance: value => { value.conditions.ref_name.include = ['refs/heads/main']; } }],
  ['missing signatures', { maintenance: value => { value.rules = value.rules.filter(rule => rule.type !== 'required_signatures'); } }],
  ['missing linear history', { maintenance: value => { value.rules = value.rules.filter(rule => rule.type !== 'required_linear_history'); } }],
  ['missing no-force protection', { maintenance: value => { value.rules = value.rules.filter(rule => rule.type !== 'non_fast_forward'); } }],
  ['missing deletion protection', { maintenance: value => { value.rules = value.rules.filter(rule => rule.type !== 'deletion'); } }],
  ['main baseline weakened', { mainPolicy: value => { value.rules = value.rules.filter(rule => rule.type !== 'required_signatures'); } }],
  ['visible bypass actor', { maintenance: value => { value.bypass_actors = [{ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' }]; } }],
  ['malformed bypass actors', { maintenance: value => { value.bypass_actors = null; } }],
  ['effective rule from wrong ruleset', { branchRules: value => { value[0].ruleset_id = 999; } }],
  ['additional main rule not mirrored', { mainRules: value => { value.push({ type: 'required_deployments' }); } }],
  ...['branch', 'compare', 'rules', 'ruleset'].map(apiError => ['API error ' + apiError, { apiError }]),
];
for (const [name, options] of cases) {
  test(`fail closed: ${name}`, async () => {
    const result = await verify(options);
    assert.equal(result.passed, false, result.info.join('\n'));
    assert.ok(result.error);
    if (options.branchRace || options.unprotectRace) assert.match(result.error.message, /stop publication/);
  });
}
for (const name of checks) {
  test(`required check cannot disappear: ${name}`, async () => {
    assert.equal((await verify({ maintenance: value => {
      const rule = value.rules.find(rule => rule.type === 'required_status_checks');
      rule.parameters.required_status_checks = rule.parameters.required_status_checks.filter(check => check.context !== name);
    } })).passed, false);
  });
}
for (const mutate of [
  value => { value.strict_required_status_checks_policy = false; },
  value => { value.do_not_enforce_on_create = true; },
  value => { value.required_status_checks[0].integration_id = 123; },
]) {
  test('weakened required-check baseline rejected', async () => {
    assert.equal((await verify({ mainPolicy: value => mutate(value.rules.find(rule => rule.type === 'required_status_checks').parameters) })).passed, false);
  });
}

const lateMutations = [
  ['maintenance signatures removed', state => {
    state.maintenance.rules = state.maintenance.rules.filter(rule => rule.type !== 'required_signatures');
  }],
  ['maintenance effective signatures removed', state => {
    state.branchRules.splice(state.branchRules.findIndex(rule => rule.type === 'required_signatures'), 1);
  }],
  ['main effective rule changed', state => { state.mainRules.push({ type: 'required_deployments' }); }],
  ['main baseline signatures removed', state => {
    state.mainPolicy.rules = state.mainPolicy.rules.filter(rule => rule.type !== 'required_signatures');
  }],
  ['maintenance enforcement disabled', state => { state.maintenance.enforcement = 'disabled'; }],
  ['maintenance target changed', state => { state.maintenance.conditions.ref_name.exclude.push('refs/heads/release/0.6'); }],
  ['maintenance visible bypass added', state => { state.maintenance.bypass_actors = [{ actor_type: 'RepositoryRole', actor_id: 5 }]; }],
  ['main baseline visible bypass added', state => { state.mainPolicy.bypass_actors = [{ actor_type: 'RepositoryRole', actor_id: 5 }]; }],
  ['all observed policies weakened together', state => {
    for (const value of [state.mainPolicy, state.maintenance]) {
      value.rules = value.rules.filter(rule => rule.type !== 'required_signatures');
    }
    for (const value of [state.mainRules, state.branchRules]) {
      value.splice(value.findIndex(rule => rule.type === 'required_signatures'), 1);
    }
  }],
];
for (const [name, onCompletion] of lateMutations) {
  test(`stable SHA and still protected: fail on late ${name}`, async () => {
    // Neither SHA nor protected=true changes; only policy is changed at the
    // final branch response, reproducing the independent review counterexample.
    const result = await verify({ onCompletion });
    assert.equal(result.branchReads, 2);
    assert.equal(result.passed, false);
    assert.match(result.error.message, /changed during verification; stop publication/);
    assert.deepEqual(result.info, []);
  });
}
for (const lateAPIError of ['baseline', 'maintenance-ruleset', 'main-rules', 'maintenance-rules']) {
  test(`fail closed on completion API error: ${lateAPIError}`, async () => {
    const result = await verify({ lateAPIError });
    assert.equal(result.branchReads, 2);
    assert.equal(result.passed, false);
    assert.match(result.error.message, /Late API unavailable/);
    assert.deepEqual(result.info, []);
  });
}
test('maintenance success requires all four policy completion reads', async () => {
  const result = await verify();
  assert.equal(result.passed, true, result.error);
  for (const [kind, key] of [['rules', 'main'], ['rules', 'release/0.6'], ['ruleset', 20402161], ['ruleset', 123]]) {
    assert.equal(result.calls.filter(call => call[0] === kind && call[1] === key).length, 2);
  }
});
test('effective-rule ordering alone is not a policy change', async () => {
  const result = await verify({ onCompletion: state => {
    state.mainRules.reverse();
    state.branchRules.reverse();
  } });
  assert.equal(result.passed, true, result.error);
});
