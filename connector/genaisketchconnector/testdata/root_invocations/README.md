# Root Invocation Compatibility Corpus

`v074-output.json` records the released default-off output of commit
`a36569b478c8206c9c7031533f513371e79ef42e` (v0.7.4).
It covers 61 existing fixture files, 115 replay inputs and eight existing
configuration profiles: 920 cases with initial, replay and next-window output.

The file records SHA-256 digests and byte lengths for metrics, top-k snapshots,
structured log messages and fields, and every serialized summary document.
Metric attribute order uses `canonicalCompatibilityAttributes` from the existing
regression harness. Log timestamps supplied by the logging backend are excluded;
the message, level and complete structured fields are retained. Summary bytes
are compared without removing fields. Ignored batches have no forwarded metrics.

## Provenance

The capture used a fresh `git archive` of the base, not the feature worktree.
Every one of the archive's 391 files was verified against its Git blob, then
included in a SHA-256 source manifest. Only the new test harness was copied into
that archive; no production source or original fixture was changed.

- Pristine source manifest: `cb3a519e330c6544278b17c2e14ffd3bcaf091626c5e8581bf778e2013a02de4`.
- Capture harness: `c826c8205edbbc9873b9529b1396a6f729b83f8251197fd60a1641daf2eff08b`.
- Golden file: `c6803e605803c966ba8316c4e9c6fa0b05d27866b115037becf50d3f9f02956d`.
- Deterministic seed: `fixed-clock-120-fixed-key-fixed-epoch-no-randomness`.
- Go: `go1.26.11`; `GOENV=off`, `GOWORK=off`, `GOTOOLCHAIN=local`,
  `GOMAXPROCS=2`, `-p=2`, Nice 10.

Two independent pristine runs produced identical output and fixture maps.
Recording refuses an existing output file, a Git worktree, a changed base file,
an added production Go file, or a failed/incomplete fixture run. Normal tests
never write expected output.

## Inputs

The golden's `fixture_sha256` map is the exact file inventory. It includes all
accounting, fleet and inspect-contract JSON fixtures; all three complete identity
corpora; LiteLLM before/after, usage and subset cases; the coding-agent OTLP
fixtures; and the existing normal-output golden. Each identity corpus sends
every input row through the connector, including the historical corpus's inputs.
The frozen historical identity expectations remain unchanged.

The new test-only traces also cover the legacy agent root, an ADK `invoke_runner`
root, a workflow root using `gen_ai.agent.name`, a remote CLIENT/INTERNAL pair
together and split, and an invocation below an HTTP parent. Gateway additions
reuse the existing native LiteLLM Chat, streaming Chat, Responses and Messages
builders and the agentgateway/Envoy served-model mappings.

All new root-summary counters must be absent when the option is off. The enabled
privacy test scans metrics, snapshots, actual structured top-k log fields,
serialized summaries and decoded sketch payloads for synthetic private content,
identity, trace-ID and span-ID sentinels. The real-binary integration test sends
OTLP over gRPC, checks unchanged base accounting and split-collector behavior,
and scans exported surfaces without copying content.

Run from the connector module:

```sh
go test -race -run '^TestRootInvocations(V074OutputCompatibility|NewSurfacePrivacy)$' -count=1 .
```

Run the real-binary test from the repository root after building the collector:

```sh
go test -race -tags integration -run '^TestRootInvocationsRealOTLP$' -count=1 ./integration
```

`ROOT_INVOCATIONS_TEST_BINARY` can select the built binary explicitly. All logs
and temporary data belong in an external private cache, not the source tree.
