# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Check study execution and capture controls, without freezing gateway defects."""

import argparse
import json
from pathlib import Path

from analyze import analyze_run, count, is_control


def verify(root):
    root = Path(root)
    manifest = json.loads((root / "manifest.json").read_text())
    outcomes = json.loads((root / "outcomes.json").read_text())
    report = analyze_run(root)
    errors = []
    if not outcomes or any(o["status"] != "completed" for o in outcomes) or report["excluded_runs"]:
        errors.append("one or more gateway pairs did not complete")
    if len(report["observations"]) != len(outcomes) * len(manifest["cases"]):
        errors.append("analysis does not cover every requested case")
    for row in report["observations"]:
        name = row["gateway"] + "/" + row["upstream_protocol"] + "/" + row["case"]
        surfaces = row["surfaces"]
        for surface in surfaces.values():
            if surface["reader_status"] in ("reader_error", "capture_missing"):
                errors.append(name + ": capture failed")
        control = is_control(row["case"], row["upstream_protocol"], manifest)
        cache_case = row["case"] in ("cache_write", "cache_read")
        provider = surfaces["provider_response"]
        if control or cache_case:
            if provider["final"] is not True or provider["usage_state"] != "complete":
                errors.append(name + ": provider reference lacks final input/output usage")
        if cache_case:
            cache = (provider["usage_details"]["cache_creation"] if row["case"] == "cache_write" else
                     provider["usage"]["cache_read"])
            if row["comparison_basis"] != "provider_response_metadata" or count(cache) is None or cache <= 0:
                errors.append(name + ": provider reference does not prove a positive " + row["case"] + " event")
        if not control:
            continue
        if row["provider_attempts"] != 1:
            errors.append(name + ": control did not have exactly one upstream attempt")
        observed = surfaces["client_response"]
        if observed["final"] is not True:
            errors.append(name + ": client control lacks final usage")
        if any(observed["dimensions"][d]["comparison"] != "equal" for d in ("input", "output")):
            errors.append(name + ": client control disagrees with provider usage")
        if row["case"] == "complete":
            for kind in ("gateway_accounting", "exported_telemetry"):
                value = surfaces[kind]
                if value["reader_status"] == "reader_unsupported":
                    continue
                if not value["records"] or value["usage_state"] != "complete":
                    errors.append(name + ": normal-response capture control lacks final usage: " + kind)
    return sorted(set(errors))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_directory", type=Path)
    args = parser.parse_args()
    errors = verify(args.run_directory)
    for error in errors:
        print(error)
    print("Study capture controls: " + ("FAIL" if errors else "PASS"))
    raise SystemExit(bool(errors))
