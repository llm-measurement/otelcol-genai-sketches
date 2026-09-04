# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
import os
import random
import time
import uuid

from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor


ENDPOINT = os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
INTERVAL_SECONDS = float(os.getenv("GENAI_EXAMPLE_INTERVAL_SECONDS", "1"))

MODELS = ["gpt-demo-small", "gpt-demo-large", "gpt-demo-reasoning"]
TEAMS = ["platform", "support", "research"]
PROMPTS = [
    "summarize the renewal risk",
    "draft an incident update",
    "rank candidate retrieval passages",
    "explain token spend by customer",
]
PROMPT_SUFFIXES = list(range(1, 401))
PROMPT_WEIGHTS = [1 / (rank**1.1) for rank in PROMPT_SUFFIXES]
DOCS = ["kb-100", "kb-101", "kb-204", "runbook-7", "contract-42"]
REQUEST_COUNT = 0


def configure_tracing():
    resource = Resource.create({"service.name": "genaisketch-example-app"})
    provider = TracerProvider(resource=resource)
    exporter = OTLPSpanExporter(endpoint=ENDPOINT, insecure=True)
    provider.add_span_processor(BatchSpanProcessor(exporter))
    trace.set_tracer_provider(provider)
    return trace.get_tracer("genaisketch-example-app")


def emit_span(
    tracer,
    rng=random,
    tool_count=1,
    include_usage=None,
    model=None,
    team=None,
    token_usage=None,
):
    global REQUEST_COUNT
    REQUEST_COUNT += 1
    model = model or rng.choices(MODELS, weights=[7, 2, 1])[0]
    team = team or rng.choice(TEAMS)
    base_prompt = rng.choices(PROMPTS, weights=[6, 3, 2, 1])[0]
    suffix = rng.choices(PROMPT_SUFFIXES, weights=PROMPT_WEIGHTS)[0]
    prompt = f"{base_prompt} #{suffix:03d}"
    input_tokens, output_tokens = token_usage or (
        rng.randint(80, 900),
        rng.randint(20, 450),
    )
    if include_usage is None:
        include_usage = REQUEST_COUNT % 10 != 0

    with tracer.start_as_current_span(
        "agent.run", attributes={"gen_ai.operation.name": "invoke_agent"}
    ):
        with tracer.start_as_current_span(
            "retrieval", attributes={"gen_ai.operation.name": "retrieval"}
        ):
            pass
        for _ in range(tool_count):
            with tracer.start_as_current_span(
                "tool.call", attributes={"gen_ai.operation.name": "execute_tool"}
            ):
                pass
        with tracer.start_as_current_span("genai.request") as span:
            span.set_attribute("gen_ai.operation.name", "chat")
            span.set_attribute("gen_ai.request.model", model)
            span.set_attribute("gen_ai.request.prompt", prompt)
            if include_usage:
                span.set_attribute("gen_ai.usage.input_tokens", input_tokens)
                span.set_attribute("gen_ai.usage.output_tokens", output_tokens)
            span.set_attribute("team.id", team)
            span.set_attribute("enduser.id", f"user-{rng.randint(1, 80):03d}")
            span.set_attribute("retrieval.doc_id", rng.choice(DOCS))
            span.set_attribute("request.id", str(uuid.uuid4()))

    return {
        "spans": tool_count + 3,
        "requests": 1,
        "tokens": input_tokens + output_tokens if include_usage else 0,
        "missing": int(not include_usage),
    }


def main():
    tracer = configure_tracing()
    while True:
        for _ in range(random.randint(3, 8)):
            emit_span(tracer)
        time.sleep(INTERVAL_SECONDS)


if __name__ == "__main__":
    main()
