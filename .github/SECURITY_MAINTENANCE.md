# Actions Permissions

After merging the read-only dependency updater, open **Settings > Actions >
General > Workflow permissions**, keep the default token read-only, and turn off
**Allow GitHub Actions to create and approve pull requests**. This is a manual
repository setting, not something a workflow changes.

The weekly `update-otel-collector` workflow checks dependency updates and uploads
a patch for maintainer review. It cannot create a branch, commit, PR or approval.
Release and chart publishing keep their job-scoped permissions; neither needs
this setting. Dependabot uses its own GitHub App, not the workflow token.
No new App credentials or personal access token are needed.

## Review A Dependency Update

1. Open a successful `update-otel-collector` run on `main`. Download its
   `collector-dependency-update` artifact. A run with no changes has no artifact.
2. Read `manifest.json`: it records the exact base commit, Collector version,
   changed paths and SHA-256 of `update.patch`. Verify the patch checksum with
   `shasum -a 256 update.patch` and compare it to `patch_sha256`.
3. Start a clean branch from current `origin/main`. Confirm its commit matches
   `base_sha`; if main has moved, rerun the updater on main. Review upstream
   release notes and every patch hunk. Check that only the six dependency files
   listed in `scripts/prepare_otel_update.py` change. Artifacts are proposed
   changes, not trusted executable instructions.
4. Set `PATCH` to the downloaded patch's absolute path, then check and apply it:

   ```sh
   git apply --stat "$PATCH"
   git apply --check "$PATCH"
   git apply "$PATCH"
   make check
   git diff --check
   git diff
   git add -- otel.version builder.yaml go.mod go.sum \
     connector/genaisketchconnector/go.mod connector/genaisketchconnector/go.sum
   git commit -s -S -m "Update OpenTelemetry Collector dependencies"
   ```

5. Push the branch and open a PR. All normal hosted checks, signature verification
   and DCO checks still apply. The updater does not bypass review or auto-merge.

The patch artifact expires after 14 days. Rerun the workflow on main for a fresh
patch instead of retaining stale update branches.

See GitHub's [workflow permission settings](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository#setting-the-permissions-of-the-github_token).
