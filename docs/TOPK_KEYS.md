# Rank Users And Sessions Without Identity Labels

Available from collector v0.3.0. Older releases do not gain this option when
their configuration changes; upgrade the image before enabling it.

By default, top-k ranks prompt signatures by observed input plus output tokens.
Use `topk_keys` to rank users or application sessions instead, or alongside them:

```yaml
connectors:
  genaisketch:
    topk: 20
    topk_keys:
      - field: prompt_key
      - field: user_key
        weight: tokens
      - field: session_key
        weight: requests
```

The default `user_key` reads `enduser.id` or `user.id` on spans, falling back to
those resource attributes. `session_key` reads `gen_ai.conversation.id` or
`session.id` on spans and uses the `session:v1` hash domain. This is separate from
`mcp_session_key`: one MCP connection is not necessarily one agent run.

To change a source, use the existing [field mapping](CONFIGURATION.md#field-mapping):

```yaml
fields:
  session_key:
    from_attributes: [app.session.id]
    canonicalization: text_v1
    domain: session:v1
```

At most four distinct configured hashed fields can be selected. The default
weight is `tokens`; the other choice is `requests`. Omitted or empty `topk_keys`
keeps the default prompt ranking. `topk: 0` disables every frequent-items sketch,
including tool errors. The `frequent_items` profile determines sketch capacity;
`topk` only limits the number of candidates shown.

## What Is Counted

- `tokens` adds input plus output only for model attempts with complete valid
  usage. Cache and reasoning subsets are not added again. Missing key values,
  missing usage, and zero total weight do not contribute to that sketch.
- `requests` adds one per counted model attempt with the configured key, including
  failed attempts, retries, and attempts missing usage. It does not count logical
  user requests, tool calls, or agent spans. Existing opt-in deduplication applies
  before either weight is added.

Source declarations still matter: a gateway's inferred counts can appear complete.
Read the [usage provenance guide](USAGE_PROVENANCE.md) before treating token totals
as provider consumption. Neither ranking is a bill or a showback ledger.

## Where Results Appear

Top-k results appear in structured `genaisketch topk snapshot` logs and optional
[summary exports](SUMMARY_EXCHANGE.md), never in metric labels. Snapshots already
identify the configured key in `field`. Request-weighted snapshots also carry
`weight: requests`; absence of `weight` means token weight for these model-key
rankings. The separate tool-error ranking continues to count tool-error events.

Token-weighted summary names include `top_prompts`, `top_users`, `top_sessions`,
`top_docs`, and `top_mcp_sessions`. Request weighting adds `_requests`, such as
`top_sessions_requests`. Custom configured fields use `top_key.<field>.<weight>`.
A reader must not assume an unknown measurement is a known
identity or silently mix token and request units.

The snapshot cap stays at 10,000 items across all slices and selected keys. Later
items may be omitted with `truncated: true`; metric counters and exported sketch
state are not truncated by this display cap. Each additional key allocates another
sketch per slice and retained window, plus one per exported scope window. See
[Sizing](SIZING.md).

## Compare Windows

Fleetdiff v0.3.0 or later can investigate these summary files:

```sh
fleetdiff investigate --before ./before --after ./after --expected app --flag-share 0.25
```

The users question reports tracked token contributors and their change bounds.
The sessions question reports share intervals and marks a **runaway candidate**
only when a session's after-window lower-bound share is strictly above the
threshold. It is a reason to investigate, not a diagnosed loop or automatic action.

Shares describe the weight attributed to that key, not all traffic. A session can
carry most attributed weight because other attempts have no session ID. Incomplete
producer coverage or token usage limits what can be concluded. Request-weighted
sessions remain useful when tokens are missing. No session sketch means
`cannot_determine`, not zero sessions. Hashes remain hidden by default in fleetdiff.

## Compatibility And Privacy

The default configuration preserves the earlier summary and snapshot bytes.
Adding optional top-k keys does not change the base accounting identifier.
Each non-default ranking includes a zero-valued counter named
`topk_contract.v1.<measurement>.<digest>`. This is an extraction-contract marker,
not an event count. Its digest covers the field sources, canonicalization, domain,
and weight. Payload metadata separately checks sketch kind, profile, and domain.
Do not remove these markers before combining summaries.

The summary library still rejects different measurement sets or contracts.
Fleetdiff v0.3.0 can compare the common measurements when optional rankings are absent
from some inputs; it does not fill absent sketches with zeroes. A changed contract
for a measurement present in both windows is rejected. Earlier fleetdiff versions
omit unrecognized rankings when input contracts match, but may reject an old/new
comparison. Upgrade the reader to compare across the addition of an optional key.

Session IDs, user IDs, and other key values are canonicalized and keyed-hashed
before entering sketch state. They are pseudonymous, not anonymous, and linkable
across windows under the same secret. Only an authorized operator with that secret
can re-hash known identities for lookup. Keep exports private; never turn their
hashes or original values into metric labels. Slice-label overlap validation also
applies to the configured session and user sources.
