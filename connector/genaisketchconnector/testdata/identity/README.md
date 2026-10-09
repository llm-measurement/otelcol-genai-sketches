# Pinned Identity Checks

These fixtures check 8,254 synthetic texts against frozen canonical bytes and
64-bit keyed hashes. They include U+11F41 with combining accents, U+001C through
U+001F at trimmed edges, 31-combining-mark cases, and 8,192 evenly spaced samples
from a 324,110-text Unicode 15 corpus. Both files record the source SHA-256 and
selection method. No operator data is included.

The public key `sketchkit-identity-vector-secret-v1` is test data only.

## Two Dependency Baselines

- `connector.json` pins the standalone connector module with x/text v0.41.0.
- `distribution.json` pins the distribution with x/text v0.42.0. Its bytes match
  sketchkit's `vectors/identity_guard/text_v1.json`.

The case IDs and input bytes are identical. The expected canonical bytes and
hashes differ for the upstream NFC correction described in
[Upgrading](../../../../docs/UPGRADING.md). This difference already exists in the
two build graphs; these tests change no runtime behavior.

CI explicitly selects the distribution baseline when testing from `dist`, which
uses the dependencies selected by the real collector build. Tests never select
goldens based on the installed dependency version and never regenerate them.

```sh
GOWORK=off go -C connector/genaisketchconnector test -run '^TestIdentityDrift' -count=1 -v
GOWORK=off make dist
GOWORK=off GENAI_IDENTITY_BASELINE=distribution go -C dist test -mod=readonly \
  github.com/llm-measurement/otelcol-genai-sketches/connector/genaisketchconnector \
  -run '^TestIdentityDrift' -count=1 -v
```

## Format And Updates

Each JSON file declares `schema_version`, `unicode_version`, `profile`, `domain`,
`hash_algorithm`, `key_utf8`, `provenance` and `cases`. Each case has a stable `id`,
`input_utf8_hex`, `canonical_hex` and `digest_hex`. Hex is lowercase. Hashes are
the first eight bytes of HMAC-SHA256 over `domain || 0x00 || canonical_bytes`,
rendered as 16 hexadecimal characters. The collector's actual
canonicalize-and-hash method is checked, alongside its canonicalizer output.

For an intentional update, compute new results over the committed inputs in a
separate file, review the per-case changes, and preserve existing case IDs and
inputs. Append new cases to both baselines. Updating or removing an existing
baseline requires a **new** `- Identity change:` entry in `CHANGELOG.md` naming
the affected inputs, the cause and how operators handle older summaries. Include
the handling in the upgrade and release notes. Do not refresh goldens merely to
make a dependency update pass.

This is sampled regression coverage. The tests also deliberately perturb expected
canonical bytes and hashes to confirm that identity changes fail the check.
