# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Container-side driver; fixed local endpoints and no automatic retries."""

import argparse
import http.client
import json
from pathlib import Path
import socket
import sqlite3
import threading
import time

import client
import gateways
from provider import CASES, PROTOCOLS


class PortkeyLogs:
    def __init__(self):
        self.ready = threading.Event()
        self.records = []
        self.status = None
        self.conn = http.client.HTTPConnection("gateway", 8787, timeout=45)
        self.thread = threading.Thread(target=self.read, daemon=True)

    def read(self):
        try:
            self.conn.request("GET", "/log/stream")
            response = self.conn.getresponse()
            self.status = response.status
            self.ready.set()
            size = 0
            while response.status == 200 and size < 2_000_000:
                line = response.readline(65537)
                size += len(line)
                if not line or len(line) > 65536:
                    break
                if line.startswith(b"data:"):
                    value = json.loads(line[5:])
                    if isinstance(value, str):
                        value = json.loads(value)
                    self.records.append(value)
        except (OSError, ValueError, http.client.HTTPException):
            self.ready.set()

    def start(self):
        self.thread.start()
        self.ready.wait(5)

    def stop(self):
        if self.conn.sock:
            try:
                self.conn.sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        self.conn.close()
        self.thread.join(timeout=3)
        return {"status": self.status, "records": self.records}


def bifrost_records():
    try:
        with sqlite3.connect("file:/evidence/logs.db?mode=ro", uri=True) as conn:
            conn.row_factory = sqlite3.Row
            return [dict(row) for row in conn.execute(
                "SELECT id, timestamp, status, stop_reason, number_of_retries, token_usage, "
                "prompt_tokens, completion_tokens, total_tokens, cached_read_tokens "
                "FROM logs ORDER BY timestamp DESC LIMIT 100")]
    except sqlite3.Error as exc:
        raise RuntimeError("Bifrost native log reader failed") from exc


def wait_live_idle(timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        conn = http.client.HTTPConnection("provider", 8080, timeout=5)
        try:
            conn.request("GET", "/idle")
            response = conn.getresponse()
            response.read(1024)
            if response.status == 200:
                return
            if response.status != 202:
                raise RuntimeError("live relay rejected or failed a request; exclude this pair")
        finally:
            conn.close()
        time.sleep(0.2)
    raise RuntimeError("live relay did not finish by drain deadline; exclude this pair")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("gateway", choices=gateways.GATEWAYS)
    parser.add_argument("protocol", choices=PROTOCOLS)
    parser.add_argument("case", choices=(*CASES, "cache_write", "cache_read", "ready"))
    parser.add_argument("--client-protocol", choices=PROTOCOLS, default="openai")
    parser.add_argument("--settle-seconds", type=float, default=8)
    args = parser.parse_args()
    port = gateways.PORTS[args.gateway]
    if args.case == "ready":
        for _ in range(120):
            try:
                with socket.create_connection(("gateway", port), timeout=1):
                    return
            except OSError:
                time.sleep(1)
        raise SystemExit("gateway did not become ready")
    root = Path("/evidence")
    key = (root / "master-key").read_text().strip()
    live = json.loads((root / "live.json").read_text()) if (root / "live.json").exists() else None
    if live and live.get("provider", "openai") != args.protocol:
        parser.error("upstream protocol differs from the live provider")
    if live and live.get("client_protocol", "openai") != args.client_protocol:
        parser.error("client protocol differs from the live configuration")
    if args.case in ("cache_write", "cache_read") and not (live and live.get("provider") == "anthropic"):
        parser.error("cache cases require the live Anthropic relay")
    try:
        url, headers, model = gateways.request_options(args.gateway, args.protocol, args.case, key,
                                                       live["model"] if live else None, args.client_protocol)
    except NotImplementedError:
        result = {"case": args.case, "gateway": args.gateway, "upstream_protocol": args.protocol,
                  "client_protocol": args.client_protocol, "status": "unsupported", "gateway_fault": False,
                  "unsupported_reason": "native_openai_translation_not_supported_by_study",
                  "terminal": False, "usage_events": [], "finish_reasons": [], "logical_requests": 0}
        (root / (args.case + ".json")).write_text(json.dumps(result, indent=2))
        print(json.dumps({"case": args.case, "status": "unsupported", "terminal": False}))
        return
    body = client.payload(args.client_protocol, args.case, model)
    if live and live.get("provider") == "anthropic":
        from anthropic_protocol import request_body
        body = request_body(model, args.case, args.client_protocol, live["cache_nonce"])
        if args.gateway == "agentgateway" and args.client_protocol == "openai" and args.case.startswith("cache_"):
            prefix = body["messages"][0]["content"][0]
            prefix.pop("cache_control")
            prefix["prompt_cache_breakpoint"] = {"mode": "explicit"}
    elif live:
        body.pop("max_tokens")
        body.update(max_completion_tokens=512, reasoning_effort="none", service_tier="default", store=False)
        body["messages"][0]["content"] = "Write the numbers from 1 through 200, one number per line. No other text."
    control = client.request("http://provider:8080/control", {"case": args.case})
    if control["status"] != 200:
        raise SystemExit("provider control failed")
    log_reader = PortkeyLogs() if args.gateway == "portkey" else None
    if log_reader:
        log_reader.start()
    before = bifrost_records() if args.gateway == "bifrost" else None
    started = time.time_ns()
    result = client.request(url, body, headers,
                            disconnect=args.case == "client_disconnect")
    if live:
        wait_live_idle()
    # Observe a full bounded settling period, including late records. Do not stop
    # at the first provisional row or claim a record can never arrive afterwards.
    time.sleep(args.settle_seconds)
    result.update(case=args.case, gateway=args.gateway, upstream_protocol=args.protocol,
                  client_protocol=args.client_protocol, started_ns=started, deadline_ns=time.time_ns(),
                  settle_seconds=args.settle_seconds, logical_requests=1)
    if log_reader:
        result["native"] = log_reader.stop()
        result["native_source"] = "Portkey local /log/stream"
    elif args.gateway == "bifrost":
        before_ids = {row["id"] for row in before}
        result["native"] = {"status": "read_only_sqlite", "records": [row for row in bifrost_records() if row["id"] not in before_ids]}
        result["native_source"] = "Bifrost SQLite logs table, read-only"
    (root / (args.case + ".json")).write_text(json.dumps(result, indent=2))
    print(json.dumps({"case": args.case, "status": result["status"], "terminal": result["terminal"]}))


if __name__ == "__main__":
    main()
