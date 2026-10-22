# Per-Model Summary Splits

The opt-in `model_usage` block keeps reported input, output and cache components
by approved public model ID, with request coverage and four input-tier bins.
It lets an upgraded reader distinguish priceable complete splits from missing
or invalid detail. Existing metrics and base summary accounting are unchanged.

## Enable After Upgrading Readers

Available since collector v0.4.1 and chart 0.4.1. Upgrade every summary reader to
fleetdiff v0.10.0 or later, with support for `model_usage` version 1 and
`input-tiers-v1`, before enabling it. Older readers reject the added block.
Keep the option absent during a rolling reader
upgrade; do not mix enabled and disabled summaries in one comparison. The producer
uses released sketchkit v0.4.0 in both Go modules, with no library replacement.

```yaml
connectors:
  genaisketch:
    slices:
      - name: by_model
        keys: [gen_ai.request.model]
    summary_export:
      directory: /var/lib/collector/summaries
      producer_id: collector-a
      scope_id: application-a
      key_id: application-key-1
      interval: 5s
      model_usage:
        slice: by_model
        models:
          gateway-alias-a: public-model-a
          gateway-alias-b: public-model-b
```

The selected slice must contain only `gen_ai.request.model`. Model mapping is
explicit: an unmapped name becomes `__unmapped__`; missing, oversized and existing
slice-overflow routes become their fixed buckets. No raw model/deployment name is
implicitly copied into the summary. Keep the mapping and routing configuration
consistent between producers; both are bound into a separate compatibility ID.
This mapping does not rewrite metric labels: existing slice labels remain
[bounded and cleartext](../README.md#privacy-and-limits). Use non-sensitive model
and route names in the selected slice.

Legacy requested-model mapping requires each observed name to identify one stable
public model. Do not assume this for dynamic aliases that can select different
models through routing or fallback. Legacy request-model attribution remains the
unchanged default when `model_usage.identity` is absent or empty; the served-model
opt-in began in v0.5.0. Native Messages and combined LiteLLM v2 require v0.6.0.

## Helm Opt-In

`summaryExport.modelUsage.enabled` defaults to `false`, independently of summary
file export. Disabled model-usage settings do not appear in the rendered collector
configuration. After upgrading readers, configure an
existing model-only slice and explicitly approved public IDs:

```yaml
summaryExport:
  enabled: true
  producerID: collector-a
  scopeID: application-a
  keyID: application-key-1
  modelUsage:
    enabled: true
    slice: by_model
    models:
      gateway-alias-a: claude-sonnet-5-5
      gateway-alias-b: gpt-6.1-sol
```

The chart's default `by_model` slice is model-only. If you customize
`connector.slices`, the selected name must still occur exactly once and contain
only `gen_ai.request.model`. The map may be empty (all observed names then use
reserved buckets); it must not be null. Aliases are limited to 256 UTF-8 bytes,
with at most 5,000 map entries. Mapping aliases are private configuration: Helm
values and the rendered ConfigMap contain them, while summary files contain only
approved public IDs or fixed reserved buckets. Restrict access to configuration
as well as summary storage.

## Served Identity Opt-In In v0.5.0

The optional `model_usage.identity` selects a version-pinned served-model
profile; absent or empty keeps legacy request-model attribution. In Helm these
fields are `summaryExport.modelUsage.identity` and `responseModels`; absent fields
are omitted, and disabled settings leave rendering unchanged. Use collector v0.5.0
and chart 0.5.0, paired with fleetdiff v0.11.0, for the profiles in this table.

| Identity | Exact input namespace for `models` |
| --- | --- |
| `agentgateway-1.6.0-chat-v1` | Span `gen_ai.response.model` |
| `envoy-ai-gateway-1.1.0-chat-v1` | Span `gen_ai.response.model` |
| `litellm-1.104.0-chat-v1` | Span `litellm.provider.model` |
| `litellm-1.104.0-chat-stream-v1` | Span `litellm.provider.model`, gateway-selected only |
| `litellm-1.104.0-responses-v1` | Span `litellm.provider.model` |
| `litellm-1.104.0-v1` | Span `litellm.provider.model`, with the matching Chat or Responses rule below |

LiteLLM nonstreaming Chat, Responses and the combined profile require a non-null
`response_models` map (Helm: `responseModels`), which may be empty. Its keys are exact `gen_ai.response.model`
values, separate from the primary map's namespace. A known selected provider model
may be priced when the response-model attribute is absent; a present response must
be explicitly mapped to the same approved public ID. Present empty or invalid
response-model attributes stay unpriced. Equality with
`litellm.model_group` does not prove an alias rewrite and does not authorize pricing.
Map provider model names only: pricing a route alias is unsafe under dynamic
routing. Verified raw and fallback provider responses can be priced when the maps
agree. The streaming Chat profile instead uses the gateway-selected model,
provider snapshot not confirmed: its response-model attribute repeats the
dispatch name even when the provider omits a model. It deliberately ignores that
attribute. The combined profile retains the map for its nonstreaming Chat and
Responses paths, but ignores it for streaming Chat. Omit `response_models` for
the standalone streaming Chat profile, both other gateways and
legacy mode. Both maps use the existing entry, name-length and public-ID limits.
Approve dated snapshot
names individually, with no prefix matching; missing, unmapped, ambiguous or
conflicting served identity stays in unpriced reserved buckets, never inheriting
an alias's price.

### LiteLLM Telemetry And Auto Routing

The LiteLLM profiles require the pinned gateway's stock `otel` callback in
latest-experimental GenAI semantic-convention mode. Configure the callback in
LiteLLM, and set the environment variable on the LiteLLM process:

```yaml
litellm_settings:
  callbacks: [otel]
```

```sh
export OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
```

Legacy telemetry mode stringifies `llm.is_streaming`; these profiles reject that
non-boolean value and leave the observation unpriced. The captured spans have no
explicit telemetry-mode marker, so the collector cannot confirm this prerequisite
from a mode attribute. Verify the gateway configuration before enabling the
profile; do not infer its mode from an absent attribute.

Default nonstreaming Auto Routing can echo the requested alias in
`gen_ai.response.model`.
Such a response remains unpriced, even if the selected provider model is mapped.
To preserve the provider response name, set the option inside each relevant Auto
Routing model entry:

```yaml
model_list:
  - model_name: routing-alias
    litellm_params:
      model: auto_router/complexity_router
      complexity_router_config:
        return_raw_model_name: true
```

This shows the option's placement, not a complete router configuration. Keep the
router's other required settings. Global `litellm_settings.return_model_name`
and `litellm_settings.return_raw_model_name` did not preserve the provider name
in the pinned checks. For nonstreaming Chat and Responses, map each returned
snapshot in `response_models`; do not add route aliases merely to increase priced
coverage.

Served identity is span-local, with no resource-attribute fallback. The existing
selected request-model slice remains the admission guard, including its overflow
decision; it is not replaced by a served-model slice. Served profiles additionally
retain at most `max_slices` public-model rows per summary window (Helm:
`connector.maxSlices`). Admission is first-seen: once full, new public IDs go to
`__overflow__`, while admitted rows continue accumulating and are never evicted
within that window. Fixed reserved buckets do not consume the public-row budget.
This bound supplements, rather than replaces, the existing default slice-overflow
behavior.

### Verified Request Scope

The agentgateway and Envoy profiles cover streaming and nonstreaming Chat,
including recovery. Their pinned spans have no stream marker, so this widens the
verified scope without changing either profile's behavior or contract ID.

For a LiteLLM gateway serving mixed Chat and Responses traffic, choose
`litellm-1.104.0-v1`; use [v2](#combined-litellm-v2-in-v060) when including Messages. One
producer then uses one contract for nonstreaming Chat, streaming Chat and
Responses. It requires the exact native call-type and route pair on every span,
even when an operation name is present. For Chat, typed
`gen_ai.request.stream=true` selects the streaming rule; false or absence selects
the nonstreaming rule. Malformed, duplicate or conflicting markers remain
unpriced. No span-name inference or cross-span identity lookup is used.

```yaml
model_usage:
  slice: by_model
  identity: litellm-1.104.0-v1
  models:
    openai/gpt-6.1-sol: gpt-6.1-sol
  response_models:
    gpt-6.1-sol: gpt-6.1-sol
```

Use the exact values emitted by the pinned gateway and approve any snapshot
names explicitly. This fragment belongs under `summary_export`; keep the
existing request-model slice. The combined contract binds all three rules and
both maps. It differs from each standalone contract, so do not mix old and new
summaries in one comparison. V1 remains unchanged and rejects Anthropic Messages.

The individual rules and standalone profiles remain available:

- `litellm-1.104.0-chat-v1` covers nonstreaming Chat. It continues to reject true
  or malformed stream flags. In intact spans from the required callback mode,
  absence of `gen_ai.request.stream` represents nonstreaming; this interpretation
  does not extend to filtered or transformed captures. Paired native markers
  `llm.request.type=acompletion` and `http.route=/v1/chat/completions` identify Chat
  only when `gen_ai.operation.name` is absent, not present empty or invalid.
- `litellm-1.104.0-chat-stream-v1` requires both native Chat markers and a typed
  `gen_ai.request.stream=true`. It uses the selected-model basis described above,
  including recovery. A response-model attribute adds no provider confirmation.
- `litellm-1.104.0-responses-v1` requires `llm.request.type=aresponses` together
  with `http.route=/v1/responses`, for streaming and nonstreaming requests. Span
  names are not an API discriminator: the pinned callback can call a Responses
  span "chat". A present response model must agree through the explicit map;
  streaming preserves the inner provider snapshot, while nonstreaming alias
  echoes remain unpriced. An absent response model permits selected-model
  attribution. Malformed or conflicting stream markers remain unpriced.

Copilot has no verified served-identity profile. With agentgateway, an absent
response model also loses native usage in the captured span, even when usage
appears on the wire. With Envoy, callers must request `include_usage` to obtain
native stream usage; interrupted streams produced no correlated span in these
checks. Model identity is not a completion signal, and these profiles do not
establish finality of reported usage.

The opt-in produces a new opaque `model_usage.contract_id` binding the profile,
maps and admission policy, without changing the summary or model-usage format
version. Review the profile and both applicable maps, then obtain and explicitly
approve the new ID using the procedure below. Pass that exact ID to the reader
with `--model-usage-contract`; a mismatched expected ID is rejected, and legacy
and served contracts cannot be mixed. The unchanged format version is not approval
of a new attribution contract.

## Combined LiteLLM v2 In v0.6.0

Collector/image v0.6.0 and chart 0.6.0 add `litellm-1.104.0-v2`: one producer and
one contract for the disjoint union of the four existing standalone LiteLLM
rules. Every span must have an exact, unique native call-type/route pair:

| Native `llm.request.type` | Native `http.route` | Existing rule selected |
| --- | --- | --- |
| `acompletion` | `/v1/chat/completions` | Chat; typed native `gen_ai.request.stream=true` selects streaming Chat |
| `aresponses` | `/v1/responses` | Responses, in either stream mode |
| `anthropic_messages` | `/v1/messages` | Messages, with its nonstreaming/streaming distinction below |

Missing, malformed, duplicate or crossed native markers reject attribution even
when `gen_ai.operation.name=chat` is present. The selected rule retains all its
typed, duplicate and conflicting stream-flag checks. There is no fallback to
span names, resource attributes, request aliases or route groups for identity.

```yaml
# Requires collector/image v0.6.0 and chart 0.6.0.
model_usage:
  slice: by_model
  identity: litellm-1.104.0-v2
  models:
    openai/approved-dispatch: approved-chat-model
    anthropic/approved-dispatch: approved-messages-model
  response_models:
    approved-chat-snapshot: approved-chat-model
    approved-messages-snapshot: approved-messages-model
```

Place this fragment under `summary_export`, retain the existing request-model
slice, and replace placeholders with individually approved exact names. In Helm,
set `summaryExport.modelUsage.identity` and `responseModels`. The bounded,
non-null response map is required, and `{}` is valid. Nonstreaming Chat,
Responses (including streaming), and nonstreaming Messages verify a present
response against it; streaming Chat and streaming Messages ignore response-model
echoes and use selected identity only. Absent responses retain each rule's
selected-only behavior.

V2 binds all four rules and both exact maps in a new contract. Approve its ID
before comparison; do not combine v2 summaries with v1, standalone or legacy
summaries. V1's contract and behavior, all standalone rules, default outputs and
the summary format remain unchanged. V0.5.0 does **not** support v2; changing
Helm values alone does not upgrade a runtime. This union adds identity coverage,
not new usage completeness or streaming completion guarantees.

## Native Messages Profile In v0.6.0

`litellm-1.104.0-messages-v1` is the standalone profile for native Anthropic
Messages through pinned LiteLLM 1.104.0. Use collector/image v0.6.0 and chart
0.6.0; installed v0.5.0 does **not** support it. The same rule is included in
combined v2. Combined `litellm-1.104.0-v1` and its contract remain unchanged and
still reject Messages.

The same stock `otel` callback and `gen_ai_latest_experimental` setting described
above are required. Every span must carry the exact native pair
`llm.request.type=anthropic_messages` and `http.route=/v1/messages`, even when a
Chat operation name is present. A span named "chat", a client header, or a
resource attribute cannot supply either missing marker. Map the span-local
`litellm.provider.model` exactly through `models`, without prefix removal or
requested-route fallback.

- Nonstreaming: false or absent `gen_ai.request.stream` selects this rule. A
  present `gen_ai.response.model` must map to the same approved public ID through
  `response_models`; an absent attribute permits selected-model-only attribution.
- Streaming: a typed `gen_ai.request.stream=true` is required. Attribution is
  selected-model-only and ignores `gen_ai.response.model`, which echoed dispatch
  rather than independently confirming the provider snapshot in the pinned probe.
- Malformed, duplicate or conflicting native/legacy stream flags stay unpriced.
  A legacy-only `llm.is_streaming=true` is not enough to establish streaming.

`response_models` (Helm: `responseModels`) is required and may be `{}`; it has the
same 5,000-entry, 256-byte-name and public-ID bounds as other response maps. An
empty map leaves present nonstreaming responses unpriced, but does not prevent
selected-only attribution for absent responses or valid native streaming.

```yaml
# Requires collector/image v0.6.0 and chart 0.6.0; not collector v0.5.0.
model_usage:
  slice: by_model
  identity: litellm-1.104.0-messages-v1
  models:
    anthropic/approved-dispatch: approved-public-model
  response_models:
    approved-provider-snapshot: approved-public-model
```

This fragment belongs under `summary_export`; placeholders must be replaced by
individually approved exact names. Review and approve the new contract ID using
the procedure below. Do not mix Messages and combined/other-profile summaries.

Pinned native ordinary, default/raw Auto Routing and recovery observations kept
selected dispatch visible. Nonstreaming default Auto Routing preserved the
provider snapshot in native telemetry even when the client saw a route alias;
the Chat/Responses alias behavior above must not be projected onto Messages.
Recovery uses the selected recovery model and, for nonstreaming, its separately
approved response snapshot. Native spans did not expose a deployment/API-base
field, so this profile neither requires one nor infers deployment identity.

The probe also found a completed, deliberately malformed nonstreaming provider
response without a `model` field that produced neither a correlated inference
span nor an exact spend row. The collector's absent-response rule only applies
to spans it actually receives; it cannot repair that telemetry gap. Pre-content
stream recovery did not export every failed physical attempt as an inference
span. An interrupted stream had no final provider usage receipt. Selected model,
reported token completeness and request completion remain separate facts: this
profile adds no streaming completion/finality guarantee and does not invent
missing usage. Translated backends, other providers/versions, and actual Claude
Code traffic are not established by these synthetic native-path checks.

## Obtain The Expected Contract

Review and approve the exact model map, ordered
slice definitions, resource fallback, `max_slices`, and accounting configuration.
Do not generate this approval from incoming model labels or from a received
summary. Keep the approved configuration and verified producer binary/image under
operator control. For Helm, review the rendered collector configuration too.

Use that configuration in an isolated local producer with private export storage
and local-only receiver/exporter endpoints. Validate it, then let it emit a window
(an empty window suffices; no model API call is needed):

```sh
./dist/otelcol-genai-sketches validate --config=./approved-collector.yaml
./dist/otelcol-genai-sketches --config=./approved-collector.yaml
```

Stop the local producer and copy one of **its own** exports to
`./trusted-local-export.json`. Do not substitute a file from the comparison input
or another unverified producer. For the example inventory above, extract the
contract using Python's standard library:

```sh
set -eu
MODEL_USAGE_CONTRACT="$(python3 - ./trusted-local-export.json <<'PY'
import json, re, sys
with open(sys.argv[1], "rb") as source:
    raw = source.read(8 * 1024 * 1024 + 1)
if len(raw) > 8 * 1024 * 1024:
    raise SystemExit("trusted export exceeds the envelope limit")
doc = json.loads(raw)
expected = {"producer_id": "collector-a", "scope_id": "application-a",
            "key_id": "application-key-1"}
if any(doc.get(key) != value for key, value in expected.items()):
    raise SystemExit("trusted export does not match the approved inventory")
usage = doc.get("model_usage") or {}
contract = usage.get("contract_id", "")
if (usage.get("version") != 1 or usage.get("histogram_id") != "input-tiers-v1"
        or not isinstance(contract, str)
        or not re.fullmatch(r"model-usage-v1:[0-9a-f]{64}", contract)):
    raise SystemExit("trusted export has no supported model-usage contract")
print(contract)
PY
)"
printf '%s\n' "$MODEL_USAGE_CONTRACT"
```

Adjust the three inventory IDs to the approved configuration. Store the printed
ID alongside that approval and pass it to fleetdiff with
`--model-usage-contract "$MODEL_USAGE_CONTRACT"`. This extraction is not proof of
map approval or producer authenticity; its authority is the controlled producer
and reviewed configuration. Reapprove and regenerate the ID after map, routing,
or accounting changes. Do not automatically accept a new ID from arriving files.
Model-by-key/team scopes and workload-map attribution are not available from
these marginal summaries.

## Coverage And Combination

The original acceptance and duplicate-suppression decision remains authoritative.
Each accepted attempt contributes its reported component totals, field-quality
counts and declared input/output provenance. A complete split requires all four
components, consistent subset/partition checks, and a known origin for an all-zero
input/output pair. Missing cache detail is counted as missing, never inserted as
zero. The input histogram uses inclusive upper edges 100,000, 200,000 and 272,000.

Rows live on retained summary windows. Metric-slice eviction does not erase or
reassign past totals. Newer cumulative snapshots replace older ones; replay does
not add a second copy. Different extension contracts or mixed presence fail.
Updates check counts and the envelope byte budget before publishing state. An
opt-in input batch is accepted atomically, so retrying a rejected batch cannot
count its already-processed prefix.

| Producer | Reader | Result |
| --- | --- | --- |
| Option absent | Existing Summary Exchange v1 | Original bytes and accounting contract |
| Option enabled | Reader without model-usage support | Rejected; upgrade reader before opt-in |
| Collector v0.4.1, option enabled | fleetdiff v0.10.0+ | Validated model-usage v1/input-tiers-v1 splits and coverage |
| Mixed option/contract settings | Any upgraded reader | Rejected as incompatible |

## Limits

These are source-reported model attempts, not authenticated billing receipts.
Reserved buckets cannot be priced. The bounded model slice and 8 MiB envelope
limit still apply. The producer reserves the configured sketches' maximum encoded
size and maximum-width counters at startup, plus a small envelope metadata
allowance; model-row bytes are tracked exactly. This can reach the byte budget
before the current sparse sketches fill it, without serializing a full summary
for each span.
There are no model-by-key or model-by-team intersections. Snapshot replay is
exactly checked; repeated raw spans retain the configured Bloom-based deduplication
semantics. Validate resource cost for the configured workload before enabling.
