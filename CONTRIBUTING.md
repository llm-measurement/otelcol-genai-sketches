# Contributing

Questions, bug reports, and focused improvements are welcome. Start with an
[issue](https://github.com/llm-measurement/otelcol-genai-sketches/issues/new/choose)
for a behavior change or a new integration. Include the question you need to
answer, the input attributes available, and the expected accounting behavior.
Report vulnerabilities through [Security](SECURITY.md), not a public issue.

## Signed and signed-off commits

Every pull-request commit needs both:

- A cryptographic signature that GitHub verifies, to establish commit provenance.
- A `Signed-off-by: Name <email>` trailer matching the commit author's name and
  email. This records your certification under the
  [Developer Certificate of Origin](https://developercertificate.org/) that you
  have the right to contribute the work under this project's Apache-2.0 license.

A signature and a sign-off serve different purposes; neither replaces the other.
The `commit-signatures` and `dco` checks name every failing commit. A passing final
commit does not cover earlier commits. The DCO check exempts only GitHub's own
verified branch-update merge commits; ordinary merges and bots still need sign-offs.

### Set up SSH signing

Use Git 2.34 or later. Use an existing signing key or create one with a passphrase;
do not overwrite an existing key:

```sh
ssh-keygen -t ed25519 -C "your-verified-email@example.com" -f "$HOME/.ssh/id_ed25519_signing"
eval "$(ssh-agent -s)"
ssh-add "$HOME/.ssh/id_ed25519_signing"
```

Add the contents of `~/.ssh/id_ed25519_signing.pub` to GitHub under **Settings >
SSH and GPG keys > New SSH key**, choosing **Signing key**. Upload only the public
`.pub` file; a key registered only for authentication is not enough.

From this repository, configure your author identity and signing. Use an email
verified on GitHub; your GitHub-provided no-reply email also works.

```sh
git config --local user.name "Your Name"
git config --local user.email "your-verified-email@example.com"
git config --local gpg.format ssh
git config --local user.signingkey "$HOME/.ssh/id_ed25519_signing.pub"
git config --local commit.gpgsign true
git commit -s -S -m "Describe the change"
```

`-s` adds the DCO trailer; `-S` creates the cryptographic signature. GPG signatures
verified by GitHub are also accepted. Read the DCO before signing off.

### Fix your own commits

For the latest commit, use `git commit --amend -s -S --no-edit`. For a branch
containing only your own contributions, fetch the current base and re-sign it:

```sh
git fetch origin
git rebase --force-rebase --signoff --gpg-sign origin/main
git push --force-with-lease
```

The rebase command rewrites commits even when the branch is already up to date.
These commands change commit IDs. Coordinate before rewriting a shared branch.
Use the name and email matching your commit author identity, and ask other authors
to fix their own commits; do not add their certification for them. After pushing,
confirm **Verified** on each commit and passing `commit-signatures` and `dco`
checks. Both checks reject incomplete API results, including PRs over 250 commits.

See GitHub's [SSH signing setup](https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key#telling-git-about-your-ssh-key),
[adding an SSH signing key](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/adding-a-new-ssh-key-to-your-github-account),
and [signing commits](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits).

## Development

### Identity compatibility

CI checks canonical bytes and keyed hashes against the fixed test corpus in
[`testdata/identity`](connector/genaisketchconnector/testdata/identity/README.md).
It runs with both the connector module's dependencies and the built
distribution's dependencies. A dependency update must pass both baselines.
CI never regenerates the expected identities.

If a reviewed change intentionally changes an existing baseline, add a new
`- Identity change:` entry to `CHANGELOG.md` explaining the affected inputs,
the cause, and how operators handle older summaries. Include that migration
in `docs/UPGRADING.md` and the release notes, and review the golden diff with
the dependency change. A PR that edits an existing baseline without the new
changelog entry fails CI.

### Local checks

Use Go 1.26.9 or later and Make. Docker with Compose v2 is needed for the demo
and container checks; Helm is needed for chart checks. Run from the repository
root with parent Go workspaces disabled:

```sh
GOWORK=off make check
GOWORK=off make dist
GOWORK=off make test-integration
python3 -m unittest discover -s examples -p test_dashboard.py
python3 -m unittest discover -s examples/app -p test_topk.py
python3 -m unittest discover -s examples/integrations/litellm -p 'test_*.py'
```

For packaging or demo changes:

```sh
make helm-check
make production-image
sh examples/demo.sh up
sh examples/demo.sh investigate
sh examples/demo.sh topk
sh examples/demo.sh down
```

The default demo is synthetic and needs no provider credentials. Provider trials
are opt-in, can cost money, and are not required for ordinary contributions.
Run `GOWORK=off make tidy` only for dependency changes and inspect both modules'
diffs. Do not include generated `dist/` or cache files.

The integration suite covers OTLP-to-Prometheus behavior, gRPC and HTTP shadow-mode
fan-out, bounded overflow, deterministic eviction, restart stability, tree locality,
and sentinel scans across metric, label, and structured-log surfaces. The offline
coding-agent replay has separate tagged checks:

```sh
GOWORK=off go -C connector/genaisketchconnector test -race -tags tracelab_replay ./...
python3 -B -m unittest discover -s examples/tracelab -p 'test_*.py'
```

See [Sizing](docs/SIZING.md) and [Upgrading](docs/UPGRADING.md) before a production
rollout, and [Benchmarks](docs/BENCHMARKS.md) for reproduction commands.

## Change Scope

Prefer existing helpers and small compositions of standard libraries. Keep
accounting, configuration, privacy, and compatibility changes explicit. Add
focused tests for the behavior changed and update the relevant public guide.
Preserve default configuration and wire formats unless the change explains and
tests a migration. Format Go with `gofmt` and retain existing license headers.

A new recipe or example adds one row to the README's guide table; its detail lives in its own README.

Separate static checks, synthetic execution, and real-provider observations in
reports. Include commands, versions, expected results, actual results, and limits;
a successful run does not by itself establish accuracy or production capacity.

## Safe Reproductions

Use synthetic spans and non-sensitive slice values. Never post hashing secrets,
credentials, raw prompts, user/session IDs, private traces, summary exports, or
unredacted logs. Keyed hashes remain pseudonymous and linkable, not anonymous.
Provide a minimal redacted configuration and synthetic fixture instead. Avoid
personal filesystem paths and unrelated environment dumps.

Pull requests should state what changed, why, how it was checked, and any
remaining compatibility or operational risks. See [Support](SUPPORT.md) for the
release policy and [Accounting](docs/ACCOUNTING.md) for measurement semantics.
