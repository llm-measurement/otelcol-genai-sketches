# Which Sessions Explain The June 2 Token Increase?

**June 1 -> June 2: recorded tokens rose 17% (+136m), with 35% more model steps
and slightly smaller steps on average (13% fewer tokens per step). Three session
increases added 171m tokens; two had no recorded weight on June 1.**

The comparison includes every recorded user label and model on both UTC days.
No traffic was injected. Recorded on 2026-10-08 with the replay tool and released
`fleetdiff v0.5.0` (`b214e8466291879aa7862f7d26875778ee20ea95`), on macOS arm64.
[Run the example](../README.md#run-it); read the consolidated
[Data and Limits](../README.md#data-and-limits) before interpreting these results.

## More Steps, Smaller Average

| Recorded measure | June 1 | June 2 | Change |
| --- | ---: | ---: | ---: |
| Model steps | 4,719 | 6,378 | +35.16% |
| Input plus output tokens | 780,233,161 | 916,393,815 | +136,160,654 (+17.45%) |
| Tokens per step | 165,338.67 | 143,680.44 | -13.10% |
| Cache-read / input share | 96.78% | 95.10% | -1.68 percentage points |

fleetdiff's symmetric arithmetic split assigns **+256,331,344 tokens** to
the higher step count and **-120,170,690** to the lower average. The two terms
sum to the recorded increase. This is an arithmetic decomposition.

## Sessions To Inspect First

| Positive session change | June 1 tokens | June 2 tokens | Increase |
| --- | ---: | ---: | ---: |
| Largest | 0 | 58,930,903 | +58,930,903 |
| Second | 0 | 56,510,164 | +56,510,164 |
| Third | 63,920,144 | 119,166,052 | +55,245,908 |
| Combined | 63,920,144 | 234,607,119 | +170,686,975 |

These three increases exceed the net rise because all other session changes
combine to **-34,526,321 tokens**. The saved investigation includes large
declining sessions as well. The returned bounds are exact for these three
changes; the public table does not expose source identities or keyed hashes.

## Source Reconciliation

The current source-only rerun traversed all **665,453 model steps**, spanning
**8,058 user/session identity pairs** and **52 pseudonymous user labels**.
The source contains **743,819 tool objects**. The replay visited **305 daily
windows**, September 23, 2025 through July 24, 2026; 286 contained model events.

| Source total checked | Tokens |
| --- | ---: |
| Input | 114,175,715,596 |
| Output | 391,755,566 |
| Cache-read input | 109,169,472,430 |
| Claude cache-write input, already included in input | 3,257,873,460 |

Every row has a usable model-event timestamp, and no duplicate user/trace keys
were found. Normalized input splits and Claude native-to-inclusive input checks
agree on every applicable row. One reasoning-output value exceeds its row's
output total; it remains unchanged and produces one connector
`reasoning_output/subset_violation` observation.

All 305 exported envelopes were checked against exact source-column sums,
including empty days and the subset-violation count. All **304 adjacent-window
investigations** passed:

- **4,256 exact counter-value checks:** seven counters on both sides of each pair.
- **43,680 returned ranking-bound checks:** before, after and delta bounds for
  displayed user/session candidates, using exact keyed source counts.
- Ranking total weights match the source token totals.

The source oracle, reports and completion record remain in the ignored run
directory. Its completion record contains the current binary/source digests and
check counts. Earlier run records remain intact as separate provenance.
