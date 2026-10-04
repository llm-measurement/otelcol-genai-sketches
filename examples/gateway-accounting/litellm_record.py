# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Read the documented post-call callback without changing its usage values."""

import json
import time
from pathlib import Path
from litellm.integrations.custom_logger import CustomLogger


class Record(CustomLogger):
    def save(self, kwargs, success):
        value = kwargs.get("standard_logging_object") or {}
        allowed = ("prompt_tokens", "completion_tokens", "total_tokens", "response_cost", "status", "model")
        row = {key: value[key] for key in allowed if key in value}
        metadata = value.get("metadata")
        usage = metadata.get("usage_object") if isinstance(metadata, dict) else None
        usage = usage if isinstance(usage, dict) else {}
        details = usage.get("prompt_tokens_details")
        details = details if isinstance(details, dict) else {}
        # Read known numeric fields only, never the surrounding request metadata.
        fields = {key: usage[key] for key in ("cache_read_input_tokens", "cache_creation_input_tokens")
                  if type(usage.get(key)) is int and 0 <= usage[key] < 2**63}
        nested = {key: details[key] for key in ("cached_tokens", "cache_write_tokens", "cache_creation_tokens")
                  if type(details.get(key)) is int and 0 <= details[key] < 2**63}
        if nested:
            fields["prompt_tokens_details"] = nested
        row.update(cache_fields=fields, cache_fields_source="standard_logging_object.metadata.usage_object")
        row.update(time_ns=time.time_ns(), callback_success=success,
                   source="LiteLLM standard_logging_object callback")
        with Path("/evidence/native.jsonl").open("a") as output:
            output.write(json.dumps(row, default=str) + "\n")

    async def async_log_success_event(self, kwargs, response_obj, start_time, end_time):
        self.save(kwargs, True)

    async def async_log_failure_event(self, kwargs, response_obj, start_time, end_time):
        self.save(kwargs, False)


callback = Record()
