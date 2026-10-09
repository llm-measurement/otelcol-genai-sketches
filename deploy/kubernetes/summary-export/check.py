# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Bounded, disposable Kubernetes test of the chart and released summary readers."""

import argparse
import base64
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[2]
sys.path.insert(0, str(REPO / "examples/integrations/agentgateway"))
import check as recipe  # noqa: E402
import loki_check  # noqa: E402

NODE = "kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0"
UTILITY = "docker.io/library/busybox:1.37.0-musl@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092"
IMAGE = "ghcr.io/llm-measurement/otelcol-genai-sketches@sha256:d1eb959970df82149512e47bbb6b1a699445f3e0e360a7a0f965fcd090a8aa7c"
CODE = "def SENTINEL_CODE(): return 'private source'"
FILE = "/synthetic/SENTINEL_PATH/private_source.py"
SECURITY = {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532,
            "allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
            "capabilities": {"drop": ["ALL"]}, "seccompProfile": {"type": "RuntimeDefault"}}


def command(args, *, data=None, accepted=(0,), timeout=240):
    result = subprocess.run([str(a) for a in args], input=data, capture_output=True, timeout=timeout)
    if result.returncode not in accepted:
        # Never echo raw input, Secret objects, captures or kubectl diagnostic content.
        raise RuntimeError(f"{Path(args[0]).name} {args[1]} failed ({result.returncode})")
    return result.stdout


def traffic(index, start):
    spans = []
    for i, count in enumerate(recipe.workload(index)):
        for _ in range(count):
            attributes = {"gen_ai.operation.name": "chat", "gen_ai.request.model": "recipe-model",
                          "user.id": recipe.USERS[i], "session.id": recipe.SESSIONS[i],
                          "app.workflow": recipe.WORKFLOWS[i], "device.id": recipe.DEVICE,
                          "mcp.resource.uri": recipe.RESOURCE, "test.code": CODE, "test.file": FILE}
            attrs = [{"key": k, "value": {"stringValue": v}} for k, v in attributes.items()]
            attrs += [{"key": "gen_ai.usage.input_tokens", "value": {"intValue": "100"}},
                      {"key": "gen_ai.usage.output_tokens", "value": {"intValue": "20"}}]
            spans.append({"traceId": f"{start + len(spans):032x}", "spanId": f"{len(spans) + 1:016x}",
                          "name": "synthetic model attempt", "startTimeUnixNano": str(start),
                          "endTimeUnixNano": str(start + 1000), "attributes": attrs})
    return {"resourceSpans": [{"resource": {}, "scopeSpans": [{"scope": {"name": "chart-test"}, "spans": spans}]}]}


def run(args):
    os.umask(0o077)
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=False)
    cluster = "summary-" + secrets.token_hex(4)
    kubeconfig = root / "kubeconfig"
    env = {**os.environ, "KUBECONFIG": str(kubeconfig)}
    kube = [args.kubectl, "--kubeconfig", kubeconfig, "--context", "kind-" + cluster, "-n", "summary-test"]
    processes = []
    secret = secrets.token_hex(32)

    def k(*argv, **kw):
        return command([*kube, *argv], **kw)

    def apply(doc):
        k("apply", "-f", "-", data=json.dumps(doc).encode())

    def forward(name, ports):
        log = (root / (name + "-forward.log")).open("wb")
        process = subprocess.Popen([*map(str, kube), "port-forward", "pod/" + name, *["0:" + str(p) for p in ports]], stdout=log, stderr=log)
        processes.append((process, log))
        for _ in range(60):
            text = (root / (name + "-forward.log")).read_text()
            matches = dict((int(remote), int(local)) for local, remote in re.findall(r"127.0.0.1:(\d+) -> (\d+)", text))
            if set(ports) <= matches.keys():
                return {p: f"http://127.0.0.1:{matches[p]}" for p in ports}
            if process.poll() is not None:
                break
            time.sleep(.5)
        raise RuntimeError("Loopback port forward did not become ready")

    def collector(name, pvc=False):
        settings = ["fullnameOverride=" + name, "summaryExport.enabled=true", "summaryExport.reader.enabled=true",
                    "summaryExport.producerID=app", "summaryExport.scopeID=gateway-recipe", "summaryExport.keyID=recipe-run",
                    "summaryExport.interval=1s", "connector.retentionWindows=16", "connector.windowDuration=20s",
                    "image.digest=" + IMAGE.split("@", 1)[1], "telemetry.logs.encoding=json", "connector.maxSlices=10",
                    "resources.requests.cpu=100m", "resources.requests.memory=128Mi"]
        if pvc:
            settings += ["summaryExport.storage.type=existingClaim", "summaryExport.storage.existingClaim=summary-data", "summaryExport.storage.sizeLimit=",
                         "shadow.enabled=true", "shadow.endpoint=backend:4317", "shadow.insecure=true"]
        cmd = [args.helm, "template", name, REPO / "deploy/helm/otelcol-genai-sketches", "--namespace", "summary-test",
               "-f", REPO / "examples/integrations/agentgateway/chart-values.yaml"]
        settings += ["shadow.enabled=" + str(pvc).lower()]
        for setting in settings:
            cmd += ["--set", setting]
        manifest = command(cmd)
        (root / (name + "-rendered.yaml")).write_bytes(manifest)
        k("apply", "-f", "-", data=manifest)
        k("rollout", "status", "deployment/" + name, "--timeout=180s")
        return json.loads(k("get", "pods", "-l", "app.kubernetes.io/instance=" + name, "-o", "json"))["items"][0]["metadata"]["name"]

    def permissions(pod):
        script = 'd=/var/lib/genai-sketches/exports; test "$(stat -c %a "$d")" = 700; for f in "$d"/*.json; do test -f "$f"; test "$(stat -c %a "$f")" = 600; done; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token; test -z "${GENAI_SKETCH_SECRET:-}"; test ! -e /var/run/genai-sketches/tls; test "$(id -u)" = 65532; grep -q "^CapEff:[[:space:]]*0000000000000000$" /proc/self/status'
        k("exec", pod, "-c", "reader", "--", "sh", "-ec", script)
        result = subprocess.run([*map(str, kube), "exec", pod, "-c", "reader", "--", "touch", "/var/lib/genai-sketches/exports/reader-write-probe"], capture_output=True)
        recipe.check(result.returncode != 0 and b"Read-only file system" in result.stderr, "reader export mount is writable")

    try:
        recipe.check(command([args.fleetdiff, "--version"]).startswith(b"fleetdiff v0.5.0 "), "use released fleetdiff v0.5.0")
        command([args.kind, "create", "cluster", "--name", cluster, "--image", NODE, "--kubeconfig", kubeconfig, "--wait", "180s"], timeout=360)
        apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "summary-test", "labels": {"pod-security.kubernetes.io/enforce": "restricted"}}})
        apply({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "genai-sketch-secret"}, "stringData": {"secret": secret}})
        # Test-only local PV on the disposable kind node. Kubelet applies fsGroup.
        node = cluster + "-control-plane"
        command(["docker", "exec", node, "mkdir", "-m", "0755", "/var/local/summary-test"])
        apply({"apiVersion": "v1", "kind": "PersistentVolume", "metadata": {"name": "summary-test"}, "spec": {
            "capacity": {"storage": "128Mi"}, "accessModes": ["ReadWriteOnce"], "persistentVolumeReclaimPolicy": "Retain",
            "storageClassName": "", "local": {"path": "/var/local/summary-test"}, "nodeAffinity": {"required": {
                "nodeSelectorTerms": [{"matchExpressions": [{"key": "kubernetes.io/hostname", "operator": "In", "values": [node]}]}]}}}})
        apply({"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": "summary-data"}, "spec": {
            "storageClassName": "", "volumeName": "summary-test", "accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": "128Mi"}}}})
        backend_config = (REPO / "examples/integrations/coding-agents/backend.yaml").read_text()
        for name, config, image, argv, mount in (
            ("backend", backend_config, "otel/opentelemetry-collector-contrib@sha256:fd328de2552466ad78385e1b1289c3f2402b1c45f265b252aab1955b42845ac1", ["--config=/config/config.yaml"], "/evidence"),
            ("loki", (REPO / "examples/integrations/agentgateway/loki.yaml").read_text(), "grafana/loki@sha256:1107dd5274e0ada47e42472b7a7e71f3b2a2fe878878108f3e2f9e51528f0193", ["-config.file=/config/config.yaml"], "/tmp"),
        ):
            apply({"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": name}, "data": {"config.yaml": config}})
            containers = [{"name": name, "image": image, "args": argv, "securityContext": SECURITY,
                           "volumeMounts": [{"name": "config", "mountPath": "/config", "readOnly": True}, {"name": "data", "mountPath": mount}]}]
            if name == "backend":
                containers.append({"name": "reader", "image": UTILITY, "command": ["sleep", "3600"], "securityContext": SECURITY,
                                   "volumeMounts": [{"name": "data", "mountPath": "/evidence", "readOnly": True}]})
            apply({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name, "labels": {"app": name}}, "spec": {
                "automountServiceAccountToken": False, "securityContext": {"fsGroup": 65532}, "containers": containers,
                "volumes": [{"name": "config", "configMap": {"name": name}}, {"name": "data", "emptyDir": {"sizeLimit": "128Mi"}}]}})
        apply({"apiVersion": "v1", "kind": "Service", "metadata": {"name": "backend"}, "spec": {
            "selector": {"app": "backend"}, "ports": [{"port": 4317, "targetPort": 4317}]}})
        k("wait", "--for=condition=Ready", "pod/backend", "pod/loki", "--timeout=180s")
        trial = collector("trial")
        time.sleep(3)
        permissions(trial)
        k("delete", "deployment/trial", "--wait=true")
        print("PASS: emptyDir export, non-root write access and read-only reader", flush=True)
        pod = collector("persistent", pvc=True)
        recipe.check(b"version 0.3.1" in k("exec", pod, "-c", "collector", "--", "/otelcol-genai-sketches", "--version"), "wrong collector release")
        ports = forward(pod, [4318, 8889])
        starts = []
        first = (int(time.time()) // 20 + 1) * 20
        for i in range(8):
            start = first + i * 20
            time.sleep(max(0, start + 1 - time.time()))
            starts.append(start * 10**9)
            result = json.loads(recipe.request(ports[4318] + "/v1/traces", traffic(i, starts[-1]), {"Content-Type": "application/json"}))
            recipe.check(not result.get("partialSuccess"), "trace input rejected")
            time.sleep(2)  # chart batch processor flush
            (root / f"metrics-{i}.txt").write_bytes(recipe.request(ports[8889] + "/metrics"))
            recipe.check(time.time() < start + 17, "traffic crossed window boundary")
            time.sleep(max(0, start + 22 - time.time()))
            print(f"Collected Kubernetes window {i + 1}/8", flush=True)
        time.sleep(3)
        permissions(pod)
        # Exercise exactly the operator's helper, without changing global kube context.
        result = subprocess.run([sys.executable, "-B", ROOT / "copy-windows.py", "--kubectl", args.kubectl,
                                 "--namespace", "summary-test", "--pod", pod, "--output", root / "summaries"], env=env, capture_output=True, timeout=120)
        recipe.check(result.returncode == 0, "documented readback failed")
        (root / "copy.txt").write_bytes(result.stdout)
        assignments = dict(shlex.split(line)[0].split("=", 1) for line in result.stdout.decode().splitlines()[1:])
        recipe.check(set(assignments) == {"BEFORE", "AFTER", "AS_OF"}, "readback assignments missing")
        operator_report = command([args.fleetdiff, "investigate", "--before", assignments["BEFORE"],
                                   "--after", assignments["AFTER"], "--expected", "app", "--format", "json"])
        (root / "operator-investigate.json").write_bytes(operator_report)
        command([args.fleetdiff, "scan", root / "summaries", "--expected", "app", "--baseline", "6",
                 "--as-of", assignments["AS_OF"], "--format", "json"], accepted=(3,))
        print("PASS: printed BEFORE, AFTER and AS_OF run investigate and scan", flush=True)
        (root / "collector.log").write_bytes(k("logs", pod, "-c", "collector"))
        (root / "backend.jsonl").write_bytes(k("exec", "backend", "-c", "reader", "--", "cat", "/evidence/backend.jsonl"))
        recipe.check(CODE in (root / "backend.jsonl").read_text() and FILE in (root / "backend.jsonl").read_text(), "privacy probes missing from raw input")
        loki = forward("loki", [3100])[3100]
        loki_check.request = lambda url, *a, **kw: recipe.request(url.replace("http://loki:3100", loki), *a, **kw)
        loki_check.run(root)
        recipe.verify(root, secret, starts, args.fleetdiff, span_source="synthetic")
        epoch = json.loads(next((root / "summaries").glob("*.json")).read_text())["epoch"]
        k("scale", "deployment/persistent", "--replicas=0")
        k("wait", "--for=delete", "pod/" + pod, "--timeout=90s")
        # Force real kubelet fsGroup reconciliation at remount and retain old files.
        command(["docker", "exec", node, "chmod", "0755", "/var/local/summary-test"])
        command(["docker", "exec", node, "chgrp", "0", "/var/local/summary-test"])
        k("scale", "deployment/persistent", "--replicas=1")
        k("rollout", "status", "deployment/persistent", "--timeout=180s")
        pod = json.loads(k("get", "pods", "-l", "app.kubernetes.io/instance=persistent", "-o", "json"))["items"][0]["metadata"]["name"]
        time.sleep(3)
        permissions(pod)
        listed = k("exec", pod, "-c", "reader", "--", "ls", "-1", "/var/lib/genai-sketches/exports").decode().splitlines()
        epochs = {n.split("-")[1][:-5] for n in listed if re.fullmatch(r"[0-9]{20}-[a-f0-9]{32}\.json", n)}
        recipe.check(epoch in epochs and len(epochs) == 2, "restart did not retain files and start a new epoch")
        (root / "restart.log").write_bytes(k("logs", pod, "-c", "collector"))
        print("PASS: PVC remount, 0700 directory, old/new 0600 files, new epoch; no state continuity assumed", flush=True)
        for path in root.rglob("*"):
            if not path.is_file() or path.name in {"backend.jsonl", "kubeconfig"}:
                continue
            data = path.read_bytes()
            recipe.check(recipe.no_sentinels(data) and b"SENTINEL_" not in data and secret.encode() not in data, "private value in derived test output")
            if path.suffix == ".json":
                for sketch in json.loads(data).get("sketches", {}).values():
                    recipe.check(b"SENTINEL_" not in base64.b64decode(sketch["data"]), "decoded sketch leaked a sentinel")
        print("PASS: released Kubernetes summary path and extended sentinel scans", flush=True)
    finally:
        failed = sys.exc_info()[0] is not None
        for process, log in processes:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            log.close()
        try:
            command([args.kind, "delete", "cluster", "--name", cluster, "--kubeconfig", kubeconfig], timeout=120)
        except (RuntimeError, subprocess.SubprocessError):
            if not failed:
                raise
            print("Cleanup incomplete: remove the isolated summary test cluster", file=sys.stderr)
        kubeconfig.unlink(missing_ok=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kind", default="kind")
    parser.add_argument("--helm", default="helm")
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--fleetdiff", required=True)
    parser.add_argument("--output", type=Path, required=True)
    run(parser.parse_args())
