# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Opt-in, version-pinned LiteLLM OpenTelemetry callback; see README.md."""
import json
from importlib.metadata import version

from litellm.integrations.opentelemetry import OpenTelemetry
from usage_provenance import FIELDS, PREFIX, ProviderUsage, capture

_SLOT = '_genaisketch_raw_provider_usage_v1'
_MAX_RESPONSE_CHARS = 1_048_576


class ProvenanceOpenTelemetry(OpenTelemetry):
    def __init__(self, *args, **kwargs):
        if version('litellm') != '1.102.1':
            raise RuntimeError('usage provenance callback requires LiteLLM 1.102.1')
        super().__init__(*args, **kwargs)

    def log_pre_api_call(self, model, messages, kwargs):
        # Request-local only: never correlate concurrent calls through shared state.
        kwargs.pop(_SLOT, None)

    def log_post_api_call(self, kwargs, response_obj, start_time, end_time):
        kwargs.pop(_SLOT, None)
        params = kwargs.get('litellm_params') or {}
        if (params.get('custom_llm_provider') != 'openai'
                or kwargs.get('call_type') not in ('completion', 'acompletion')
                or kwargs.get('stream')):
            return
        raw = kwargs.get('original_response')
        if not isinstance(raw, str) or len(raw) > _MAX_RESPONSE_CHARS:
            return
        try:
            body = json.loads(raw)
        except (ValueError, RecursionError):
            return
        if not isinstance(body, dict) or body.get('object') != 'chat.completion':
            return
        usage = capture(body.get('usage'))
        # Retain only two validated counts, not provider content or identifiers.
        kwargs[_SLOT] = (usage.input, usage.output)

    def set_attributes(self, span, kwargs, response_obj):
        super().set_attributes(span, kwargs, response_obj)
        attributes = {PREFIX + field + '.provenance': 'unknown' for field, _ in FIELDS}
        captured = kwargs.get(_SLOT)
        if captured is not None and not kwargs.get('stream'):
            emitted = getattr(response_obj, 'usage', None)
            if hasattr(emitted, 'model_dump'):
                emitted = emitted.model_dump()
            attributes = ProviderUsage(*captured).attributes(emitted)
        for key, value in attributes.items():
            span.set_attribute(key, value)


callback = ProvenanceOpenTelemetry()
