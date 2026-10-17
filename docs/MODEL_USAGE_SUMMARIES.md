# Per-Model Summary Splits

The opt-in `model_usage` block keeps reported input, output and cache components
by approved public model ID, with request coverage and four input-tier bins.
It lets an upgraded reader distinguish priceable complete splits from missing
or invalid detail. Existing metrics and base summary accounting are unchanged.

## Enable After Upgrading Readers

Available in collector v0.4.1 and chart 0.4.1. Upgrade every summary reader to
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
opt-in described below is not yet released.

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

## UNRELEASED: Served Identity Opt-In

The upcoming optional `model_usage.identity` selects a version-pinned served-model
profile; absent or empty keeps legacy request-model attribution. In Helm these
fields are `summaryExport.modelUsage.identity` and `responseModels`; absent fields
are omitted, and disabled settings leave rendering unchanged. This opt-in is not
available in the released collector/chart pair documented above.

| Identity | Exact input namespace for `models` |
| --- | --- |
| `agentgateway-1.6.0-chat-v1` | Span `gen_ai.response.model` |
| `envoy-ai-gateway-1.1.0-chat-v1` | Span `gen_ai.response.model` |
| `litellm-1.104.0-chat-v1` | Span `litellm.provider.model` |
| `litellm-1.104.0-chat-stream-v1` | Span `litellm.provider.model`, gateway-selected only |
| `litellm-1.104.0-responses-v1` | Span `litellm.provider.model` |

LiteLLM nonstreaming Chat and Responses additionally require a non-null
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
attribute. Omit `response_models` for streaming Chat, both other gateways and
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

LiteLLM keeps separate request profiles:

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
limit still apply; the producer reserves a small allowance for envelope metadata.
There are no model-by-key or model-by-team intersections. Snapshot replay is
exactly checked; repeated raw spans retain the configured Bloom-based deduplication
semantics. Validate resource cost for the configured workload before enabling.
