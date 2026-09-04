# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
import random
import unittest
from collections import Counter

import app
from investigate import SCENARIOS
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter


class ExampleTrafficTest(unittest.TestCase):
    def setUp(self):
        self.exporter = InMemorySpanExporter()
        self.provider = TracerProvider()
        self.provider.add_span_processor(SimpleSpanProcessor(self.exporter))
        self.tracer = self.provider.get_tracer("example-test")
        self.addCleanup(self.provider.shutdown)

    def test_demo_contains_trees_and_missing_usage(self):
        app.REQUEST_COUNT = 0
        for _ in range(10):
            app.emit_span(self.tracer, rng=random.Random(7))
        spans = self.exporter.get_finished_spans()
        counts = Counter(span.attributes["gen_ai.operation.name"] for span in spans)
        self.assertEqual(
            counts,
            {"chat": 10, "invoke_agent": 10, "execute_tool": 10, "retrieval": 10},
        )
        models = [
            span for span in spans if span.attributes["gen_ai.operation.name"] == "chat"
        ]
        self.assertEqual(
            sum("gen_ai.usage.input_tokens" not in span.attributes for span in models),
            1,
        )
        self.assertTrue(all(span.parent is not None for span in models))

    def test_investigations_have_the_documented_ground_truth(self):
        for _, tools, omit_half in SCENARIOS:
            with self.subTest(tools=tools, omit_half=omit_half):
                self.exporter.clear()
                for ordinal in range(100):
                    app.emit_span(
                        self.tracer,
                        rng=random.Random(7),
                        tool_count=tools,
                        include_usage=not omit_half or ordinal % 2 == 0,
                        token_usage=(120, 80),
                    )
                spans = self.exporter.get_finished_spans()
                models = [
                    span
                    for span in spans
                    if span.attributes["gen_ai.operation.name"] == "chat"
                ]
                self.assertEqual(len(spans), 100 * (tools + 3))
                self.assertEqual(len(models), 100)
                tokens = sum(
                    span.attributes.get("gen_ai.usage.input_tokens", 0)
                    + span.attributes.get("gen_ai.usage.output_tokens", 0)
                    for span in models
                )
                self.assertEqual(tokens, 10000 if omit_half else 20000)
                self.assertTrue(
                    all(
                        "gen_ai.usage.input_tokens" not in span.attributes
                        for span in spans
                        if span not in models
                    )
                )


if __name__ == "__main__":
    unittest.main()
