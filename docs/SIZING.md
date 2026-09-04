# Sizing

Sizing follows workload shape, not only spans per second. The main inputs are active
slice values, retained windows, sketch profiles, enabled MCP fields, top-k size, and
the number of configured slice views.

## Recorded Baseline

The closest production-shaped measurement is the fleet soak in
[Benchmarks](BENCHMARKS.md):

| Input | Recorded value |
| --- | ---: |
| Machine | Apple M4 Max, 16 logical CPUs, 64 GiB RAM; Darwin 25.5.0; Go 1.26.1 |
| Rate and duration | 10,000 spans/second for 60 minutes |
| Span mix | 30% model requests; 70% agent, tool, retrieval, MCP, and workflow |
| Slice capacity | 1,000 active values plus one overflow value |
| Windows | 10 windows of 60 seconds |
| Profiles and top-k | `small` HLL/frequent-items, `micro` Bloom, top 20 |
| Maximum RSS | 1,386.8 MiB |
| RSS growth slope | 187.6 KiB/min |
| Refused exports | 0 |

The default chart requests 1 CPU and 1 GiB and limits the pod to 2 CPUs and 2 GiB.
Its memory limiter has a 1,843 MiB hard limit and a 307 MiB spike allowance, placing
the soft limit at 1,536 MiB and leaving headroom above the recorded peak. Explicit
MiB values avoid accidentally sizing against host memory instead of a container
limit. CPU was not isolated in the recorded soak, so the CPU values are starting
points rather than measured requirements.

Do not turn the 10,000 spans/second result into a universal capacity claim. Repeat
the load test with the intended exporters, label views, profiles, and traffic mix.

## Capacity Effects

| Change | Expected effect |
| --- | --- |
| Increase `max_slices` | More cumulative counters and one sketch set per retained window for each admitted value |
| Add a configured slice | Another complete accounting view over every matching observation |
| Increase `retention_windows` | Roughly proportional growth in retained sketch state, not cumulative counters |
| Increase sketch profile | Better accuracy with more memory per active slice and window |
| Increase `topk` | Larger structured snapshots; it does not create metric labels |
| Set `topk: 0` | No frequent-items state or structured top-k snapshots |
| Enable MCP or tool-error fields | Additional sketches only for slices and windows where those values appear |
| Enable deduplication | One Bloom filter per active slice and retained window |

`max_slices` is global for ordinary values. Each configured slice can also have one
fixed overflow state. Top-k snapshots are globally capped at 10,000 emitted items
per snapshot even when `topk * active slices` is larger.

## Choosing A Starting Point

1. Count the bounded values you intend to retain for each slice, then set
   `max_slices` above their combined steady-state count. Do not size it to unbounded
   user, prompt, document, request, or session IDs.
2. Start with `small` profiles, 10 one-minute windows, top 20, and deduplication off.
3. Start at the chart's 2 GiB memory limit for a workload near the recorded 1,000
   active values and 10,000 spans/second. Use a lower traffic environment to measure
   before reducing it.
4. Alert on collector refused spans, restarts, memory-limiter refusals, and
   `overflow="true"` traffic.
5. Run at least one window rotation and one expected traffic burst before accepting
   the size. A long soak is appropriate before a high-volume rollout.

## Scale-Out

One aggregation shard must see all observations whose HLL or top-k state must be
combined. Do not place interchangeable replicas behind random load balancing and
then add their distinct estimates. Instead, use stable routing to disjoint tenant or
workload shards. Prometheus may add exact counters across those shards; merging
sketch state requires a sketch-aware consumer.
