# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Run pinned real gateways against a deterministic, isolated synthetic provider."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import subprocess
import sys
import time
import uuid

import gateways
from provider import CASES, PROTOCOLS

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[1]


def command(args, log, timeout=240):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    with log.open("ab") as output:
        output.write(result.stdout)
    if result.returncode:
        raise RuntimeError("container operation failed; see private run log")
    return result.stdout


def run_pair(root, name, protocol, cases, settle, live=None, client_protocol="openai"):
    root.mkdir(mode=0o700)
    master_key = "sk-" + secrets.token_hex(24)
    (root / "master-key").write_text(master_key)
    if live and live.get("provider") == "anthropic":
        live = {**live, "cache_nonce": secrets.token_hex(16)}
    config = gateways.compose(root, name, protocol, master_key, live)
    (root / "compose.json").write_text(json.dumps(config, indent=2))
    project = "gateway-study-" + uuid.uuid4().hex[:12]
    compose = ["docker", "compose", "-p", project, "-f", str(root / "compose.json")]
    log = root / "operations.log"
    outcome = {"gateway": name, "upstream_protocol": protocol, "client_protocol": client_protocol,
               "project": project, "cases": [], "status": "running"}
    try:
        command(compose + ["up", "-d", "provider", "collector", "gateway"], log, timeout=600)
        command(compose + ["run", "--rm", "--no-deps", "-T", "driver", name, protocol, "ready",
                           "--client-protocol", client_protocol], log, timeout=180)
        for case in cases:
            print(f"{name} / {protocol}: {case}", flush=True)
            before = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
            command(compose + ["run", "--rm", "--no-deps", "-T", "driver", name, protocol, case,
                               "--settle-seconds", str(settle), "--client-protocol", client_protocol], log, timeout=150 + settle)
            logs = command(compose + ["logs", "--no-color", "--no-log-prefix", "--since", before, "gateway"], log)
            (root / (case + ".logs")).write_bytes(logs)
            outcome["cases"].append(case)
            value = json.loads((root / (case + ".json")).read_text())
            control = case in ("complete", "stream_standard") or case == "stream_complete" and (protocol == "openai" or live)
            if control and (value["status"] != 200 or not value["terminal"]):
                raise RuntimeError("normal-response control failed; do not classify faults")
        outcome["status"] = "completed"
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        outcome.update(status="setup_or_control_failed", error_type=type(exc).__name__)
        print(f"{name} / {protocol}: stopped; private diagnostics retained", flush=True)
    finally:
        try:
            logs = command(compose + ["logs", "--no-color"], log)
            (root / "stack.log").write_bytes(logs)
        finally:
            command(compose + ["down", "--remove-orphans", "--volumes"], log)
        (root / "outcome.json").write_text(json.dumps(outcome, indent=2))
    return outcome


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateways", nargs="+", choices=gateways.GATEWAYS, default=list(gateways.GATEWAYS))
    parser.add_argument("--protocols", nargs="+", choices=PROTOCOLS, default=list(PROTOCOLS))
    parser.add_argument("--cases", nargs="+", choices=(*CASES, "cache_write", "cache_read"), default=list(CASES))
    parser.add_argument("--client-protocol", choices=PROTOCOLS, default="openai")
    parser.add_argument("--settle-seconds", type=int, default=8, choices=range(5, 121))
    parser.add_argument("--output", type=Path)
    parser.add_argument("--live", action="store_true", help="explicit opt-in to paid provider calls")
    parser.add_argument("--key-file", type=Path, help="credential file outside the checkout; never copied")
    parser.add_argument("--max-estimated-usd", type=str, help="total estimated spend guard across selected gateways")
    parser.add_argument("--cut-mode", choices=("drain", "cancel"), default="drain")
    args = parser.parse_args()
    live = None
    if args.live:
        from decimal import Decimal, InvalidOperation
        from live import LIVE_CASES, MODEL, guards
        try:
            budget = Decimal(args.max_estimated_usd or "0")
            if not budget.is_finite() or not 0 < budget <= 40:
                raise ValueError
        except (InvalidOperation, ValueError):
            parser.error("live runs require --max-estimated-usd greater than 0 and at most 40")
        if len(args.protocols) != 1:
            parser.error("live runs require exactly one upstream protocol")
        if args.protocols == ["anthropic"]:
            import anthropic_protocol
            supported, model, rates = anthropic_protocol.LIVE_CASES, anthropic_protocol.MODEL, anthropic_protocol.RATES
        else:
            supported, model, rates = LIVE_CASES, MODEL, guards.MANIFEST["rates_usd_per_million"]
        if not set(args.cases) <= set(supported):
            parser.error("live runs require explicit live-supported --cases")
        if not args.key_file or not args.key_file.is_file() or REPO in args.key_file.resolve().parents:
            parser.error("live runs require an existing credential file outside the checkout")
        if len(set(args.gateways)) != len(args.gateways):
            parser.error("duplicate gateways would invalidate the spend allocation")
        live = {"key_file": args.key_file.resolve(), "model": model,
                "provider": args.protocols[0], "client_protocol": args.client_protocol,
                "max_estimated_usd": str(budget / len(args.gateways)), "max_provider_attempts": 12,
                "cut_mode": args.cut_mode, "rates_usd_per_million": rates}
    elif args.key_file or args.max_estimated_usd:
        parser.error("credentials and spend limits require explicit --live")
    if not live and set(args.cases) & {"cache_write", "cache_read"}:
        parser.error("cache_write and cache_read require a live Anthropic trial")
    if args.client_protocol == "anthropic" and args.protocols != ["anthropic"]:
        parser.error("native Anthropic clients require --protocols anthropic")
    cache_cases = set(args.cases) & {"cache_write", "cache_read"}
    if cache_cases and cache_cases != {"cache_write", "cache_read"}:
        parser.error("include both cache_write and cache_read")
    if not {"complete", "stream_complete"} <= set(args.cases):
        parser.error("include complete and stream_complete controls before fault cases")
    if "anthropic" in args.protocols and "stream_standard" not in args.cases:
        parser.error("Anthropic runs also require stream_standard as the normal streaming control")
    controls = ["complete", *(["stream_standard"] if "stream_standard" in args.cases else []), "stream_complete"]
    cases = controls + [case for case in dict.fromkeys(args.cases) if case not in controls and case not in cache_cases]
    if cache_cases:
        cases += ["cache_write", "cache_read"]
    os.umask(0o077)
    root = (args.output or REPO / ".cache" / ("gateway-study-" + uuid.uuid4().hex[:12])).resolve()
    if (REPO / ".cache").resolve() not in root.parents:
        parser.error("keep private evidence in a new directory under repository .cache")
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    sources = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
               for p in ROOT.rglob("*") if p.is_file() and p.suffix in (".py", ".json", ".md", ".yaml")}
    manifest = {"created_ns": time.time_ns(), "machine": platform.machine(), "system": platform.system(),
                "python": platform.python_version(), "images": gateways.IMAGES, "sources": sources,
                "mode": "live_provider" if live else "deterministic_mock",
                "billing_verified": False, "cases": cases, "settle_seconds": args.settle_seconds,
                "client_protocol": args.client_protocol}
    dependencies = ROOT.parent / "integrations/litellm/provider-trial"
    manifest["guard_sources"] = {name: hashlib.sha256((dependencies / name).read_bytes()).hexdigest()
                                 for name in ("relay.py", "manifest.json")}
    if live:
        manifest["live"] = {k: v for k, v in live.items() if k != "key_file"}
        manifest["max_total_estimated_usd"] = str(budget)
        manifest["max_total_provider_attempts"] = 12 * len(args.gateways)
    else:
        manifest["provider_calls_billed"] = 0
    (root / "manifest.json").write_text(json.dumps(manifest, indent=2))
    print("Private evidence:", root, flush=True)
    results = []
    for name in args.gateways:
        for protocol in args.protocols:
            results.append(run_pair(root / (name + "-" + protocol), name, protocol, cases,
                                    args.settle_seconds, live, args.client_protocol))
        if live and results[-1]["status"] != "completed":
            break
    (root / "outcomes.json").write_text(json.dumps(results, indent=2))
    if any(hashlib.sha256((ROOT / path).read_bytes()).hexdigest() != digest for path, digest in sources.items()):
        raise RuntimeError("study source changed during this run; retain it as a development run")
    from analyze import write_report
    write_report(root)
    return 0 if all(value["status"] == "completed" for value in results) else 1


if __name__ == "__main__":
    sys.exit(main())
