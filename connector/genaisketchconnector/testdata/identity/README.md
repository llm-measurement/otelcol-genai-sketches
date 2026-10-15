# Pinned Identity Checks

These fixtures check 8,254 synthetic texts against frozen canonical bytes and
64-bit keyed hashes. They include U+11F41 with combining accents, U+001C through
U+001F at trimmed edges, 31-combining-mark cases, and 8,192 evenly spaced samples
from a 324,110-text Unicode 15 corpus. Both files record the source SHA-256 and
selection method. No operator data is included.

The public key `sketchkit-identity-vector-secret-v1` is test data only.

## Two Dependency Baselines

- `connector.json` pins the standalone connector module with x/text v0.42.0.
- `distribution.json` retains the distribution baseline captured with x/text
  v0.42.0, now checked unchanged against the v0.43.0 distribution.

The case IDs, inputs, expected canonical bytes and hashes now agree. The
standalone migration below accounts for the upstream NFC correction described
in [Upgrading](../../../../docs/UPGRADING.md#sketchkit-030-in-this-source-candidate).

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

## Reviewed Standalone Migration

The v0.41.0 standalone baseline is preserved byte-for-byte in
`connector-xtext-0.41.json`, SHA-256
`690eb6e6493e2e3a82a9e4dd882c59beac3bce338628fde3ecb43ee9adadba75`.
The test pins that digest and requires exactly the following eight canonical
and hash changes, with every input and other output unchanged. Corrected outputs
are taken from the already-frozen distribution corpus, not regenerated to hide
failures. Full input and canonical hex are retained in the two JSON files.

| Case ID (after `explicit_kawi_11f41_`) | Old digest | Corrected digest |
| --- | --- | --- |
| `acute` | `3b726aaf5527e4f6` | `d6d90ae87a2e7092` |
| `deprecated_acute` | `3b726aaf5527e4f6` | `d6d90ae87a2e7092` |
| `reorder` | `953c92db0ae9bbcf` | `21019bc4498b3513` |
| `ordered` | `953c92db0ae9bbcf` | `21019bc4498b3513` |
| `grave_reorder` | `4a0f139d4ae09495` | `1719d9969f42c520` |
| `grave_ordered` | `4a0f139d4ae09495` | `1719d9969f42c520` |
| `leading_29` | `c5a6a91e289ba0b2` | `345bc98ceaba8240` |
| `trailing_31` | `06e3d616c334bdfa` | `63f48b1c77a1b554` |

The older normalizer could collapse U+11F41 plus acute/grave to unrelated Greek
U+1F45/U+1F43. The corrected outputs preserve U+11F41 with the canonically ordered
marks and stream-safe joiners. Edge controls U+001C..U+001F and the separate
31-combining-mark cases remain unchanged in Go. This sampled list is not an
exhaustive characterization of the upstream normalization fixes.
