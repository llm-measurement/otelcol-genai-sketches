# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
"""Plot the captured reconciliation JSON; requires matplotlib only."""

import json
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt


def main():
    here = Path(__file__).resolve().parent
    payload = json.loads((here / "results.json").read_text())
    assert payload["synthetic_only"] is True
    rows = {row["scenario"]: row for row in payload["results"]}
    target = here.parent / "images"
    target.mkdir(exist_ok=True)
    cases = [rows["baseline"], rows["more-tools"]]
    fig, ax = plt.subplots(figsize=(9, 4.5))
    x = range(2)
    ax.bar(
        [i - 0.18 for i in x],
        [r["emitted_spans"] for r in cases],
        0.36,
        label="emitted spans",
        color="#487ba5",
    )
    ax.bar(
        [i + 0.18 for i in x],
        [r["collector"]["requests"] for r in cases],
        0.36,
        label="counted model requests",
        color="#c97730",
    )
    ax.set_xticks(list(x), ["1 tool per run", "8 tools per run"])
    ax.set_ylabel("count per synthetic batch")
    ax.set_title("More tool calls; still 100 model requests")
    ax.legend(frameon=False)
    for bars in ax.containers:
        ax.bar_label(bars, padding=3)
    ax.set_ylim(0, 1300)
    fig.tight_layout()
    fig.savefig(target / "tool-spans.png", dpi=150)
    plt.close(fig)

    cases = [rows["baseline"], rows["missing-usage"]]
    fig, axes = plt.subplots(1, 2, figsize=(10, 4.5))
    for i, row in enumerate(cases):
        axes[0].bar(
            i - 0.18,
            row["synthetic_token_truth"],
            0.36,
            color="#487ba5",
            label="known synthetic tokens" if i == 0 else None,
        )
        axes[0].bar(
            i + 0.18,
            row["collector"]["tokens"],
            0.36,
            color="#c97730",
            label="reported tokens counted" if i == 0 else None,
        )
    axes[0].set_ylabel("tokens")
    axes[0].set_ylim(0, 30000)
    axes[0].legend(frameon=False, fontsize=9)
    axes[1].bar(list(x), [r["collector"]["missing"] for r in cases], color="#a74b59")
    axes[1].set_ylabel("requests missing usage / 100")
    axes[1].set_ylim(0, 100)
    for ax in axes:
        ax.set_xticks(list(x), ["Baseline", "Half omitted"])
        for bars in ax.containers:
            ax.bar_label(bars, padding=3, fmt="%.0f")
    fig.suptitle("Reported tokens fell. Synthetic consumption did not.")
    fig.tight_layout()
    fig.savefig(target / "missing-usage.png", dpi=150)
    plt.close(fig)


if __name__ == "__main__":
    main()
