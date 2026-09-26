# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
import importlib.util
import json
import pathlib
import types
import unittest
from unittest.mock import patch

from usage_provenance import PREFIX


class BaseLogger:
    def set_attributes(self, span, kwargs, response_obj):
        pass


module = types.ModuleType('litellm.integrations.opentelemetry')
module.OpenTelemetry = BaseLogger
spec = importlib.util.spec_from_file_location(
    'tested_provenance_callback', pathlib.Path(__file__).with_name('provenance_callback.py'))
callback_module = importlib.util.module_from_spec(spec)
with patch.dict('sys.modules', {'litellm.integrations.opentelemetry': module}), \
        patch('importlib.metadata.version', return_value='1.102.1'):
    spec.loader.exec_module(callback_module)


class Span:
    def __init__(self):
        self.attributes = {}

    def set_attribute(self, key, value):
        self.attributes[key] = value


class CallbackTests(unittest.TestCase):
    def kwargs(self, usage=None, **updates):
        result = {'call_type': 'acompletion', 'litellm_params': {'custom_llm_provider': 'openai'},
                  'original_response': json.dumps({'object': 'chat.completion', 'usage': usage,
                                                   'choices': ['PRIVATE_SENTINEL']})}
        result.update(updates)
        return result

    def attributes(self, kwargs, emitted=None):
        span = Span()
        callback_module.callback.set_attributes(span, kwargs, types.SimpleNamespace(usage=emitted))
        return list(span.attributes.values())

    def test_source_capture_before_normalization(self):
        fixture = json.loads(pathlib.Path(__file__).with_name('usage-cases.json').read_text())
        for case in fixture['cases']:
            with self.subTest(case=case['name']):
                streaming = case['name'].startswith('stream_')
                kwargs = self.kwargs(case['raw_usage'], stream=streaming)
                callback_module.callback.log_post_api_call(kwargs, None, None, None)
                expected = ['unknown', 'unknown'] if streaming else case['expected']
                self.assertEqual(self.attributes(kwargs, case['emitted_usage']), expected)
                self.assertNotIn('PRIVATE_SENTINEL', repr(kwargs.get(callback_module._SLOT)))

    def test_interleaved_requests_and_retry_reset(self):
        a, b = self.kwargs({'prompt_tokens': 0, 'completion_tokens': 0}), self.kwargs()
        for kwargs in (a, b):
            callback_module.callback.log_post_api_call(kwargs, None, None, None)
        self.assertEqual(self.attributes(a, {'prompt_tokens': 0, 'completion_tokens': 0}), ['provider_reported'] * 2)
        self.assertEqual(self.attributes(b, {'prompt_tokens': 0, 'completion_tokens': 0}), ['unavailable'] * 2)
        callback_module.callback.log_pre_api_call(None, None, a)
        self.assertEqual(self.attributes(a), ['unknown'] * 2)

    def test_unobserved_or_unsupported_is_unknown(self):
        for change in ({'original_response': 'not-json'}, {'original_response': '[]'},
                       {'original_response': '{' * 1_048_577}, {'original_response': None},
                       {'original_response': json.dumps({'error': 'PRIVATE_SENTINEL'})},
                       {'litellm_params': {'custom_llm_provider': 'other'}},
                       {'call_type': 'embedding'}, {'stream': True}):
            kwargs = self.kwargs(**change)
            kwargs[callback_module._SLOT] = (42, 42)
            callback_module.callback.log_post_api_call(kwargs, None, None, None)
            self.assertEqual(self.attributes(kwargs), ['unknown'] * 2)

    def test_caller_metadata_cannot_claim_provenance(self):
        kwargs = self.kwargs(metadata={PREFIX + 'input.provenance': 'provider_reported'})
        callback_module.callback.log_pre_api_call(None, None, kwargs)
        self.assertEqual(self.attributes(kwargs), ['unknown'] * 2)

    def test_version_is_pinned(self):
        with patch.object(callback_module, 'version', return_value='unreviewed'):
            with self.assertRaises(RuntimeError):
                callback_module.ProvenanceOpenTelemetry()


if __name__ == '__main__':
    unittest.main()
