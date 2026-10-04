# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Small, explicit adapters for published gateway configuration surfaces."""

import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
IMAGES = json.loads((ROOT / "images.json").read_text())
GATEWAYS = ("litellm", "bifrost", "agentgateway", "portkey")
PORTS = {"litellm": 4000, "bifrost": 8080, "agentgateway": 3000, "portkey": 8787}
# Verified against the release tags in images.json, not unpinned SDK examples:
# https://github.com/BerriAI/litellm/blob/v1.104.0/litellm/proxy/anthropic_endpoints/endpoints.py
# https://github.com/maximhq/bifrost/blob/transports/v2.2.5/transports/bifrost-http/integrations/anthropic.go
# https://github.com/agentgateway/agentgateway/blob/v1.6.0/crates/agentgateway/src/llm/mod.rs
# https://github.com/agentgateway/agentgateway/blob/v1.6.0/crates/agentgateway/src/proxy/httpproxy.rs
# https://github.com/Portkey-AI/gateway/blob/v1.15.2/src/index.ts
NATIVE_PATHS = {"litellm": "/v1/messages", "bifrost": "/anthropic/v1/messages",
                "agentgateway": "/v1/messages", "portkey": "/v1/messages"}


def configuration(name, protocol, master_key, model=None):
    model = model or ("gpt-4o-mini" if protocol == "openai" else "claude-sonnet-4-20250514")
    if name == "litellm":
        return {"model_list": [{"model_name": "study-model", "litellm_params": {
            "model": protocol + "/" + model,
            "api_base": "http://provider:8080/v1" if protocol == "openai" else "http://provider:8080",
            "api_key": "synthetic-provider-key", "num_retries": 1, "timeout": 20}}],
            "router_settings": {"num_retries": 1, "retry_after": 0.1},
            "litellm_settings": {"callbacks": ["otel", "litellm_record.callback"], "turn_off_message_logging": True},
            "general_settings": {"master_key": master_key}}
    if name == "bifrost":
        return {"client": {"enable_logging": True, "disable_content_logging": True},
                "framework": {"pricing": {"pricing_url": "file:///evidence/catalog.json",
                    "model_parameters_url": "file:///evidence/parameters.json",
                    "mcp_library_url": "file:///evidence/mcp.json", "mcp_library_sync_interval": 0,
                    "live_models_sync_interval": 0}},
                "providers": {protocol: {"keys": [{"name": "synthetic", "value": "synthetic-provider-key",
                    "models": ["*"], "weight": 1}], "network_config": {"base_url": "http://provider:8080",
                    "max_retries": 1, "retry_backoff_initial": 100, "retry_backoff_max": 100,
                    "allow_private_network": True, "default_request_timeout_in_seconds": 20}}},
                "logs_store": {"enabled": True, "type": "sqlite", "config": {"path": "/evidence/logs.db"}}}
    if name == "agentgateway":
        return {"config": {"logging": {"format": "json"},
                "tracing": {"otlpEndpoint": "http://collector:4317", "randomSampling": True}},
                "binds": [{"port": 3000, "listeners": [{"routes": [{"backends": [{"ai": {
                    "name": "study", "provider": {"openAI" if protocol == "openai" else "anthropic": {}},
                    "hostOverride": "provider:8080",
                    "pathOverride": "/v1/chat/completions" if protocol == "openai" else "/v1/messages"}}], "policies": {
                    "ai": {"routes": {"/v1/messages": "messages", "*": "completions"}},
                    "retry": {"attempts": 2, "codes": [429], "backoff": "100ms"}}}]}]}]}
    return {}


def request_options(name, protocol, case, master_key, provider_model=None, client_protocol="openai"):
    if name not in GATEWAYS or protocol not in ("openai", "anthropic"):
        raise ValueError("unknown gateway or upstream protocol")
    if client_protocol not in ("openai", "anthropic"):
        raise ValueError("unknown client protocol")
    if client_protocol == "anthropic" and protocol == "openai" and name != "litellm":
        # Portkey's pinned OpenAI adapter has no Messages implementation. Bifrost
        # and agentgateway select Responses upstream, which this study does not serve.
        raise NotImplementedError("native_openai_translation_not_supported_by_study")
    headers = {}
    model = "study-model"
    path = "/v1/chat/completions"
    if name == "litellm":
        headers["Authorization"] = "Bearer " + master_key
    elif name == "bifrost":
        model = protocol + "/" + (provider_model or ("gpt-4o-mini" if protocol == "openai" else "claude-sonnet-4-20250514"))
        path = "/openai/v1/chat/completions"
    elif name == "portkey":
        model = provider_model or ("gpt-4o-mini" if protocol == "openai" else "claude-sonnet-4-20250514")
        headers.update({"Authorization": "Bearer synthetic-provider-key", "x-portkey-provider": protocol,
                        "x-portkey-custom-host": "http://provider:8080/v1",
                        "x-portkey-config": json.dumps({"retry": {"attempts": 1, "on_status_codes": [429]}})})
    elif provider_model:
        model = provider_model
    if client_protocol == "anthropic":
        path = NATIVE_PATHS[name]
        headers["anthropic-version"] = "2023-06-01"
        if name == "litellm":
            headers.pop("Authorization")
            headers["x-api-key"] = master_key
        elif name == "portkey":
            headers["x-api-key"] = "synthetic-provider-key"
    return f"http://gateway:{PORTS[name]}{path}", headers, model


def compose(root, name, protocol, master_key, live=None):
    if live and live.get("provider", "openai") != protocol:
        raise ValueError("live provider must match the configured upstream protocol")
    root = Path(root)
    model = live["model"] if live else None
    (root / "gateway.json").write_text(json.dumps(configuration(name, protocol, master_key, model), indent=2))
    base = {"read_only": True, "cap_drop": ["ALL"], "security_opt": ["no-new-privileges:true"],
            "pids_limit": 256, "mem_limit": "2g", "cpus": 2, "networks": ["study"],
            "tmpfs": ["/tmp:size=256m,mode=1777"], "user": "65532:65532",
            "logging": {"driver": "json-file", "options": {"max-size": "10m", "max-file": "2"}}}
    import os
    # Bind mounts remain private to the invoking user; containers use that UID.
    user = f"{os.getuid()}:{os.getgid()}"
    shared = [f"{ROOT}:/study:ro", f"{root}:/evidence"]
    provider = {**base, "image": IMAGES["python"]["image"], "user": user,
                "command": ["python", "-B", "/study/provider.py"], "volumes": shared}
    driver = {**base, "image": IMAGES["python"]["image"], "user": user,
              "entrypoint": ["python", "-B", "/study/driver.py"], "volumes": shared}
    capture = {"receivers": {"otlp": {"protocols": {"grpc": {"endpoint": "0.0.0.0:4317"},
               "http": {"endpoint": "0.0.0.0:4318"}}}},
               "exporters": {"otlphttp": {"endpoint": "http://provider:8080", "encoding": "json",
                    "compression": "none", "sending_queue": {"enabled": False}, "retry_on_failure": {"enabled": False}}},
               "service": {"pipelines": {"traces": {"receivers": ["otlp"], "exporters": ["otlphttp"]}}}}
    (root / "capture.json").write_text(json.dumps(capture))
    collector = {**base, "image": IMAGES["collector"]["image"], "user": user,
                 "command": ["--config=/evidence/capture.json"], "volumes": [f"{root}:/evidence:ro"]}
    gateway = {**base, "image": IMAGES[name]["image"], "user": user, "volumes": shared}
    if name == "litellm":
        gateway.update(entrypoint=["litellm"], command=["--config", "/evidence/gateway.json", "--port", "4000"],
            environment={"HOME": "/tmp", "PYTHONPATH": "/study", "PYTHONDONTWRITEBYTECODE": "1",
                "LITELLM_LOCAL_MODEL_COST_MAP": "True", "OTEL_EXPORTER": "otlp_http",
                "OTEL_ENDPOINT": "http://collector:4318", "OTEL_BSP_SCHEDULE_DELAY": "500",
                "OTEL_SEMCONV_STABILITY_OPT_IN": "gen_ai_latest_experimental"})
    elif name == "bifrost":
        gateway.update(entrypoint=["/app/main"], command=["-app-dir", "/evidence/bifrost", "-host", "0.0.0.0"])
        (root / "bifrost").mkdir(mode=0o700)
        (root / "bifrost" / "config.json").write_text(json.dumps(configuration(name, protocol, master_key, model)))
        catalog_model = model or ("gpt-4o-mini" if protocol == "openai" else "claude-sonnet-4-20250514")
        (root / "catalog.json").write_text(json.dumps({
            catalog_model: {"provider": protocol, "mode": "chat", "input_cost_per_token": 0.000001,
                            "output_cost_per_token": 0.000001}}))
        (root / "parameters.json").write_text("{}")
        (root / "mcp.json").write_text('{"servers":[]}')
    elif name == "agentgateway":
        gateway["command"] = ["-f", "/evidence/gateway.json"]
    result = {"services": {"provider": provider, "gateway": gateway, "collector": collector, "driver": driver},
              "networks": {"study": {"internal": True}}}
    if live:
        relay = "live_anthropic.py" if live.get("provider") == "anthropic" else "live.py"
        provider.update(command=["python", "-B", "/study/" + relay], networks=["study", "egress"],
                        secrets=["provider_key"])
        provider["volumes"] = [*provider["volumes"], f"{ROOT.parent / 'integrations/litellm/provider-trial'}:/trial:ro"]
        result["networks"]["egress"] = {}
        result["secrets"] = {"provider_key": {"file": str(live["key_file"])}}
        (root / "live.json").write_text(json.dumps({k: v for k, v in live.items() if k != "key_file"}))
    return result
