# Why Did Recorded Token Use Rise On June 2?

In one lab's recorded Claude Code and Codex traces, input-plus-output tokens rose
**17% (+136m)** from June 1 to June 2. There were **35% more model steps**, with
slightly smaller steps on average (**13% fewer tokens per step**). The three
largest session increases added **171m tokens**; two had no recorded tokens in
the before window. Other session changes partly offset those increases.

**Which sessions should I inspect first?** The worked comparison uses
fleetdiff v0.5.0 to rank session changes and checks the connector's historical
summaries against the released trace rows.
[Recorded results](derived/RESULTS.md) give the exact comparison and coverage.

## Run It

From this repository's root, use Go 1.26.9+, Python 3.11+, curl and
[GitHub CLI](https://cli.github.com/). No provider account or Docker is needed.
Install the checksum- and attestation-verified fleetdiff release:

```sh
mkdir -p .cache
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://raw.githubusercontent.com/llm-measurement/fleetdiff/v0.5.0/scripts/install.sh \
  -o .cache/install-fleetdiff.sh
printf '%s\n' 'f561e95b7599485f32c2d07f1a5288a4c836ca4b9a816938991ba7e08b24a70e  .cache/install-fleetdiff.sh' | shasum -a 256 -c -
sh .cache/install-fleetdiff.sh v0.5.0 .cache/fleetdiff-recipe
python3 examples/tracelab/run.py --fleetdiff .cache/fleetdiff-recipe/fleetdiff
```

The runner reuses the cached release, or downloads it when absent, and verifies
its checksum before parsing. It builds the replay tool from this checkout,
creates a private random hash key, writes daily summary windows, checks source
totals and ranking bounds, and saves investigations. It refuses to reuse an
output directory. For another run, add
`--out .cache/tracelab-v0.0.2/another-run`.

Open the worked comparison separately:

```sh
.cache/fleetdiff-recipe/fleetdiff investigate \
  --before .cache/tracelab-v0.0.2/run/windows/2026-06-01.json \
  --after .cache/tracelab-v0.0.2/run/windows/2026-06-02.json --expected tracelab
```

The runner builds the source tool with `-tags tracelab_replay`. Replay code and
its internal helpers are excluded from default collector builds; the production
Dockerfile is unchanged.

## One Accounting Path

```text
normalized model-step rows -> GenAI model spans -> connector -> summary files -> fleetdiff
```

Rows are sorted by the latest `text`, `reasoning`, `tool_call` or `usage_report`
timestamp, with a stable identity tie-break. The connector's injectable clock
advances to each row's time. Exports run at each UTC midnight, before consuming
events at that boundary. All 305 days are visited, including days with no rows.

The mapper discards prompts, source code, file paths, projects and tool payloads.
User and session keys reach the connector's existing hashing boundary; neither
becomes a metric label. Session identity includes the source user to avoid
joining unrelated sessions. Missing token fields remain absent.

| Source column | GenAI span attribute |
| --- | --- |
| `input_tokens_total` | `gen_ai.usage.input_tokens` |
| `output_tokens` | `gen_ai.usage.output_tokens` |
| `prefix_tokens` | `gen_ai.usage.cache_read.input_tokens` |
| `claude_cache_creation_input_tokens`, when present | `gen_ai.usage.cache_write.input_tokens` |
| `reasoning_output_tokens`, when present | `gen_ai.usage.reasoning.output_tokens` |
| `user` | `enduser.id` |
| `[user, session_id]` | `gen_ai.conversation.id` |

For Claude, total input already includes cache writes and cache reads. The mapper
checks that inclusive total with the
[internal normalizer](../../connector/genaisketchconnector/internal/normalization/claude.go)
and never adds cache writes again. It emits one
model span per normalized row, not one per tool call or polling event.

## Regression Checks

The replay/live-path test compares serialized summary bytes with identical clock,
key, epoch and export schedule. It covers boundaries, idle windows, missing
usage, cache writes, an invalid subset and frequent-items pruning.

```sh
go -C connector/genaisketchconnector test -race ./...
python3 -B -m unittest discover -s examples/tracelab -p 'test_*.py' -v
```

The synthetic tests need no dataset download. The full example checks all daily
envelopes and adjacent-window investigations against `source-oracle.json`, which
contains exact source-column sums and keyed counts. The oracle checks results;
it does not produce sketches or envelopes. `run.json` records source and binary
digests, platform, commands and check counts. Only a run with that completion
record has finished all checks.

## Data And Limits

We obtained these traces from [TraceLab](https://tracelab.cs.washington.edu/),
by the SyFI Lab, University of Washington. The input is the released normalized
[v0.0.2 JSONL.GZ](https://github.com/uw-syfi/TraceLab/releases/tag/v0.0.2),
pinned to source commit `61fea8f97277d0aa247cc2e4c7e31f90ed2bea1c` and compressed-file
SHA-256 `11ce51ec0a25e3d1d95b025bca2f7d1647e47571eb7cc968acd5fc64d4b4fb65`.
The pinned [dataset license](https://github.com/uw-syfi/TraceLab/blob/61fea8f97277d0aa247cc2e4c7e31f90ed2bea1c/LICENSE-DATASET.md#L1-L37)
covers released JSONL and DuckDB under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/),
separately from Apache-2.0 code. This example reads only that released file.
Generated runs carry an attribution notice; these published observations have their own
[attribution](derived/ATTRIBUTION.md).

- Release data, the hash key, source oracle, windows and full reports stay in
  ignored `.cache/` directories. Derived run files stay local;
  network access is for release/tool/module downloads only. Keep `hash-key.txt`
  private; deleting it prevents reproducing that run's hashes.
- These are one lab's released records of its own Claude Code and Codex use.
  Differences describe observed activity in that dataset. User and session
  labels are pseudonymous source identities.
- Windows use export-event timestamps. Complete replay coverage means every
  row in the supplied file was traversed. An empty day or an absent session
  means the file contains no matching row for that window.
- Returned ranking bounds apply to displayed candidates. Aliases are local to
  a measurement and may change between runs.
