# Contributing

Questions, bug reports, and focused improvements are welcome. Start with an
[issue](https://github.com/llm-measurement/otelcol-genai-sketches/issues/new/choose)
for a behavior change or a new integration. Include the question you need to
answer, the input attributes available, and the expected accounting behavior.
Report vulnerabilities through [Security](SECURITY.md), not a public issue.

## Signed commits

Every commit in a pull request must have a signature GitHub verifies. The
`commit-signatures` check lists every unverified commit, including bot and merge
commits. A signed final commit does not cover earlier unsigned commits. A
`Signed-off-by` line (`git commit -s`) is not a cryptographic signature.

For SSH signing, use Git 2.34 or later. Use an existing signing key or create one
with a passphrase; do not overwrite an existing key:

```sh
ssh-keygen -t ed25519 -C "your-verified-email@example.com" -f "$HOME/.ssh/id_ed25519_signing"
eval "$(ssh-agent -s)"
ssh-add "$HOME/.ssh/id_ed25519_signing"
```

Add the contents of `~/.ssh/id_ed25519_signing.pub` to GitHub under **Settings >
SSH and GPG keys > New SSH key**, choosing **Signing key**. Upload only the public
`.pub` file; a key registered only for authentication is not enough.

From this repository, configure signing and an email verified on your GitHub
account (your GitHub-provided no-reply email also works):

```sh
git config --local user.email "your-verified-email@example.com"
git config --local gpg.format ssh
git config --local user.signingkey "$HOME/.ssh/id_ed25519_signing.pub"
git config --local commit.gpgsign true
git commit -S -m "Describe the change"
```

After pushing, confirm **Verified** on every PR commit and a passing
`commit-signatures` check. Existing unsigned commits need to be signed again by
their contributor; adding another signed commit does not fix them. Rewriting a
shared branch requires coordination and changes commit IDs. GPG signatures
verified by GitHub also satisfy the check.

Split PRs with more than 250 commits: GitHub's PR-commit API caps the list there,
and the check rejects incomplete lists rather than approving unexamined commits.

See GitHub's [SSH signing setup](https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key#telling-git-about-your-ssh-key),
[adding an SSH signing key](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/adding-a-new-ssh-key-to-your-github-account),
and [signing commits](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits).

## Local Checks

Use Go 1.26.6 or later and Make. Docker with Compose v2 is needed for the demo
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

## Change Scope

Prefer existing helpers and small compositions of standard libraries. Keep
accounting, configuration, privacy, and compatibility changes explicit. Add
focused tests for the behavior changed and update the relevant public guide.
Preserve default configuration and wire formats unless the change explains and
tests a migration. Format Go with `gofmt` and retain existing license headers.

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
