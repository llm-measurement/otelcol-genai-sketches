# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch


class CallbackCapture(unittest.TestCase):
    def capture(self, payload, success=True):
        logger = types.ModuleType("litellm.integrations.custom_logger")
        logger.CustomLogger = object
        spec = importlib.util.spec_from_file_location("study_callback", Path(__file__).with_name("litellm_record.py"))
        module = importlib.util.module_from_spec(spec)
        with patch.dict("sys.modules", {"litellm.integrations.custom_logger": logger}):
            spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "native.jsonl"
            with patch.object(module, "Path", return_value=output):
                module.Record().save({"standard_logging_object": payload}, success)
            return json.loads(output.read_text())

    def test_cache_fields_keep_zero_and_do_not_copy_metadata(self):
        row = self.capture({"prompt_tokens": 100, "completion_tokens": 20,
            "messages": "PRIVATE_PROMPT", "metadata": {"user_api_key": "PRIVATE_KEY",
                "usage_object": {"cache_read_input_tokens": 0, "cache_creation_input_tokens": 64,
                    "customer": "PRIVATE_ID", "prompt_tokens_details": {"cached_tokens": 0,
                        "cache_write_tokens": 64, "cache_creation_tokens": 64, "prompt": "PRIVATE_TEXT"}}}})
        self.assertEqual(row["cache_fields"], {"cache_read_input_tokens": 0, "cache_creation_input_tokens": 64,
            "prompt_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 64, "cache_creation_tokens": 64}})
        self.assertEqual(row["cache_fields_source"], "standard_logging_object.metadata.usage_object")
        self.assertEqual(row["prompt_tokens"], 100)
        self.assertNotIn("PRIVATE", json.dumps(row))

    def test_invalid_cache_counts_are_not_serialized(self):
        for value in (True, -1, 2**63, "PRIVATE", {"secret": "PRIVATE"}, None):
            with self.subTest(value=value):
                row = self.capture({"metadata": {"usage_object": {"cache_read_input_tokens": value,
                    "cache_creation_input_tokens": value, "prompt_tokens_details": {"cached_tokens": value,
                        "cache_write_tokens": value, "cache_creation_tokens": value}}}})
                self.assertEqual(row["cache_fields"], {})

    def test_missing_fields_stay_missing_on_success_and_failure(self):
        for success in (False, True):
            for metadata in (None, "PRIVATE", {}, {"usage_object": "PRIVATE"},
                             {"usage_object": {"prompt_tokens_details": "PRIVATE"}}):
                row = self.capture({"metadata": metadata}, success)
                self.assertEqual(row["cache_fields"], {})
                self.assertEqual(row["callback_success"], success)
                self.assertNotIn("prompt_tokens", row)
                self.assertNotIn("PRIVATE", json.dumps(row))
