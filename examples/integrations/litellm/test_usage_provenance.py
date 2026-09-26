# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
import json
import pathlib
import unittest

from usage_provenance import capture


class ProvenanceTests(unittest.TestCase):
    def test_captured_matrix(self):
        fixture = json.loads(pathlib.Path(__file__).with_name('usage-cases.json').read_text())
        self.assertEqual(fixture['litellm_version'], '1.102.1')
        self.assertEqual(len(fixture['cases']), 6)
        for case in fixture['cases']:
            with self.subTest(case=case['name']):
                snapshot = capture(case['raw_usage'])
                got = snapshot.attributes(case['emitted_usage'], inferred_fields=case['inferred_fields'])
                self.assertEqual(list(got.values()), case['expected'])

    def test_zero_is_provider_reported_only_when_observed_at_source(self):
        zero = {'prompt_tokens': 0, 'completion_tokens': 0}
        self.assertEqual(set(capture(zero).attributes(zero).values()), {'provider_reported'})
        self.assertEqual(set(capture(None).attributes(zero).values()), {'unavailable'})

    def test_capture_survives_in_place_normalization(self):
        raw = {'prompt_tokens': 80}
        snapshot = capture(raw)
        raw['completion_tokens'] = 0
        self.assertEqual(snapshot.attributes(raw)['gen_ai_sketch.usage.output.provenance'], 'unavailable')

    def test_invalid_and_changed_counts_never_claim_provider_origin(self):
        for value in (True, -1, 1.5, '42', 1 << 63, None):
            raw = {'prompt_tokens': value}
            self.assertNotEqual(capture(raw).attributes(raw)['gen_ai_sketch.usage.input.provenance'], 'provider_reported')
        self.assertEqual(capture({'prompt_tokens': 8}).attributes({'prompt_tokens': 9})['gen_ai_sketch.usage.input.provenance'], 'unknown')

    def test_no_implicit_estimation_or_content_retention(self):
        snapshot = capture({'prompt_tokens': 2, 'private': 'PRIVATE_SENTINEL'})
        self.assertNotIn('PRIVATE_SENTINEL', repr(snapshot))
        self.assertEqual(capture(None).attributes({'completion_tokens': 3})['gen_ai_sketch.usage.output.provenance'], 'unavailable')
        with self.assertRaises(ValueError):
            snapshot.attributes({}, inferred_fields=['PRIVATE_SENTINEL'])


if __name__ == '__main__':
    unittest.main()
