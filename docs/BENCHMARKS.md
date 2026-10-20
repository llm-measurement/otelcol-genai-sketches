# Benchmarks

The fleet-shaped run accepted and exported 36 million spans in 60 minutes at
10,000 spans/second, with zero refused spans and exact request and missing-usage
accounting. All runs below used one Apple M4 Max. The dated records describe
those workloads and configurations; use the commands to size your own deployment.

## Opt-In Accounting

Recorded 2026-10-20 on Apple M4 Max, 16 logical CPUs, 64 GiB RAM, Darwin 27.0.0,
Go 1.26.9 (arm64). Precomputing the model-usage byte reserve reduced median
processing time from 142.19 to 17.13 microseconds per span with small profiles,
and from 193.22 to 19.34 with default profiles in this workload.

The baseline is commit `303b86637cc33e9624194d758b4be10ba8a16b59`.
[Raw samples, source hashes and commands](benchmarks/accounting-performance.txt)
bind both implementations to the same benchmark fixture. The serial runs used
`GOMAXPROCS=1`, five one-second samples per case, and batches of 100 spans.
Initialization is excluded. Both start with dense HLLs, occupied frequent-item
maps, one model/team, the two default slices and one top-k key.

| Sketch profiles | Path | Before, us/span | After, us/span | Before, bytes/batch | After, bytes/batch |
| --- | --- | ---: | ---: | ---: | ---: |
| small (configuration default) | Default | 6.18 | 6.34 | 629,257 | 629,257 |
| small | Summary export | 8.77 | 8.92 | 1,014,504 | 1,014,536 |
| small | Model usage | 142.19 | 17.13 | 35,285,685 | 2,076,578 |
| default | Default | 8.85 | 9.07 | 844,459 | 844,459 |
| default | Summary export | 11.36 | 11.67 | 1,337,304 | 1,337,336 |
| default | Model usage | 193.22 | 19.34 | 64,596,258 | 2,399,345 |

Default-path median time was 2.5% higher, with identical allocation counts.
These are in-process measurements, not OTLP throughput or deployment capacity.
The raw log also includes deduplication at its design limit: the repaired path
counts every later request, while the baseline increasingly suppresses them, so
that timing comparison does not represent equivalent accounting work.

```bash
cd connector/genaisketchconnector
GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off GOMAXPROCS=1 CGO_ENABLED=1 \
  go test -p 1 -run '^$' -bench '^BenchmarkAccountingPerformance$' \
  -benchmem -benchtime=1s -count=5 -cpu=1
```

## In-Process Connector Load

Recorded 2026-07-05 on an Apple M4 Max with 16 logical CPUs and 64 GiB RAM, running
Darwin 25.5.0 and Go 1.26.1.

```bash
GOWORK=off make load
```

| Workload | Throughput | Time/op | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: | ---: |
| Mixed 10,000-span batch | 528,924 spans/s | 18,906,034 ns | 27,881,483 | 467,524 |
| 1,000 active slices | 116,549 spans/s | 8,579,963 ns | 67,276,339 | 159,166 |
| Frequent rotation | 139,253 spans/s | 7,181,023 ns | 66,829,875 | 156,132 |

The rotation workload completed 165 rotations per second.

## Sustained 60-Minute Run

Recorded 2026-07-08 on the same Apple M4 Max system. The collector received a
continuous 10,000 spans/second for 60 minutes.

```bash
GOWORK=off make soak
```

| Measurement | Result |
| --- | ---: |
| Attempted / accepted / exported | 36,000,000 / 36,000,000 / 36,000,000 |
| Refused spans | 0 |
| Requests missing token usage | 3,600,000 (10%) |
| Maximum collector RSS | 1,279.7 MiB |
| RSS slope | 255.6 KiB/min |
| Export latency p99 | 37.241042 ms |
| Rotation latency p99 | 41.698417 ms |
| Top-k snapshots observed | 3,426 |

Overflow was observed and retained under the single configured overflow label.

An earlier full-duration calibration used a 1,024 MiB RSS limit and did not pass that
limit: maximum RSS was 1,281.2 MiB. Traffic acceptance, export, missing-usage
accounting, overflow, and sentinel checks all passed in that run. The measured working
set was used to set the subsequently exercised 1,536 MiB limit.

## Fleet-Shaped 60-Minute Run

Recorded 2026-07-11 on the same Apple M4 Max system. The generator emitted 3.6
million traces containing 10 spans each at a continuous 10,000 spans/second. Tree
depth cycled from 3 through 8. The span mix was 30% LLM, 20% agent, 20% tool, 10%
retrieval, 10% MCP, and 10% workflow.

Identity attributes were resource-only, prompts followed a Zipf distribution with
unbounded ordinals, 10% of LLM spans omitted usage, and 5% of trees were selected for
overflow traffic after a short active-only warmup.

```bash
GOWORK=off make soak-fleet
```

| Measurement | Result |
| --- | ---: |
| Attempted / accepted / exported | 36,000,000 / 36,000,000 / 36,000,000 |
| Emitted LLM spans / counted requests | 10,800,000 / 10,800,000 |
| Planted / counted missing-token requests | 1,080,000 / 1,080,000 |
| Active / overflow LLM requests | 10,269,150 / 530,850 |
| Maximum collector RSS | 1,386.8 MiB |
| RSS slope | 187.6 KiB/min |
| Export latency p99 | 84.263250 ms |
| Rotation latency p99 | 23.372666 ms |
| Top-k snapshots observed | 3,027 |

The 530,850 overflow requests are 4.915% of LLM spans rather than exactly 5% because
the deterministic overflow schedule starts after the active-only warmup. Generator
accounting and collector observation matched exactly. No full-duration non-passing
fleet-shaped run was recorded.

## Reproducing Short Runs

Use shorter durations to validate the harness before committing an hour:

```bash
GOWORK=off SOAK_DURATION=2m SOAK_RATE_PER_SEC=1000 SOAK_BATCH_SIZE=100 make soak
GOWORK=off SOAK_DURATION=2m SOAK_RATE_PER_SEC=1000 SOAK_BATCH_SIZE=100 make soak-fleet
```

The harness records duration, rate, batch size, span mix, tree depth, burst pattern,
identity placement, accepted and refused spans, process RSS, latency, overflow,
missing usage, and structured snapshot observations in its output header and summary.
