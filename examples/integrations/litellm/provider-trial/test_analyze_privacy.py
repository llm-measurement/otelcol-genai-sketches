# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Offline report-boundary regressions; all scan values are synthetic."""
import inspect
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import analyze
import client
import verify


class ReportPrivacy(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        for name in ('evidence', 'results', 'summaries', 'fleetdiff'):
            (self.root / name).mkdir()
        (self.root / 'evidence/ledger.jsonl').touch()
        environment = patch.dict(os.environ, {}, clear=True)
        environment.start()
        self.addCleanup(environment.stop)

    def test_analyzer_accepts_no_secrets_or_environment(self):
        self.assertEqual(tuple(inspect.signature(analyze.analyze).parameters), ('root',))
        with patch.object(analyze.os, 'environ', {}):
            self.assertEqual(analyze.analyze(self.root), {
                'provider_attempts': 0, 'prior_reconciled_cost_usd': '0',
                'cost_plus_outstanding_reserves_usd': '0',
                'observed_usage_estimated_usd': '0', 'cases': []})

    def test_clean_report_bytes_are_unchanged(self):
        expected = {'provider_attempts': 0, 'prior_reconciled_cost_usd': '0',
                    'cost_plus_outstanding_reserves_usd': '0',
                    'observed_usage_estimated_usd': '0', 'cases': [],
                    'privacy': {'secret_hit_files': [], 'prompt_sentinel_hit_files': []}}
        report = analyze.write_report(self.root, 'synthetic-provider', 'synthetic-hash')
        self.assertEqual(report, expected)
        self.assertEqual((self.root / 'analysis.json').read_bytes(),
                         json.dumps(expected, indent=2).encode())

    def test_exact_serialized_bytes_are_checked_before_write(self):
        report = {'cases': [], 'text': 'quotes: " and backslash: \\'}
        target = self.root / 'analysis.json'
        target.write_text('preserved')

        def checked(root, data, secret, hash_secret):
            self.assertEqual(root, self.root)
            self.assertEqual(target.read_text(), 'preserved')
            self.assertIsInstance(data, bytes)
            self.assertEqual(data, json.dumps(report, indent=2).encode())
            self.assertEqual((secret, hash_secret), ('synthetic-provider', 'synthetic-hash'))

        with patch.object(analyze, 'analyze', return_value=report) as builder:
            with patch.object(analyze, 'check_privacy', side_effect=checked) as guard:
                analyze.write_report(self.root, 'synthetic-provider', 'synthetic-hash')
            builder.assert_called_once_with(self.root)
            guard.assert_called_once()
        self.assertEqual(target.read_bytes(), guard.call_args.args[1])

    def test_raw_and_json_escaped_output_refuse_without_overwrite(self):
        for value in ('synthetic-provider-"\\\n\u2603', 'synthetic-hash-"\\\t\u2603', client.SENTINEL):
            for location in ('provider', 'hash', 'sentinel'):
                if (value == client.SENTINEL) != (location == 'sentinel'):
                    continue
                with self.subTest(location=location, value_length=len(value)):
                    secret = value if location == 'provider' else 'synthetic-provider'
                    hash_secret = value if location == 'hash' else 'synthetic-hash'
                    target = self.root / 'analysis.json'
                    target.write_text('preserved')
                    for data in (value.encode(), json.dumps({'untrusted': value}).encode()):
                        with self.assertRaisesRegex(ValueError, '^Privacy scan failed; no analysis report written$'):
                            analyze.check_privacy(self.root, data, secret, hash_secret)
                    with patch.object(analyze, 'analyze', return_value={'untrusted': value}):
                        with self.assertRaisesRegex(ValueError, '^Privacy scan failed; no analysis report written$'):
                            analyze.write_report(self.root, secret, hash_secret)
                    self.assertEqual(target.read_text(), 'preserved')

    def test_each_capture_surface_and_scan_value_refuses_independently(self):
        for relative in ('evidence/capture.json', 'results/case/client.json',
                         'summaries/window.json', 'fleetdiff/report.json',
                         'stack-logs.txt', 'analysis.json'):
            captured = self.root / relative
            captured.parent.mkdir(parents=True, exist_ok=True)
            captured.touch()
            for value in ('synthetic-provider', 'synthetic-hash', client.SENTINEL):
                with self.subTest(path=relative, scan_value_length=len(value)):
                    original = Path.read_bytes

                    def read_capture(path):
                        return json.dumps({'data': value}).encode() if path == captured else original(path)

                    with patch.object(Path, 'read_bytes', new=read_capture):
                        with self.assertRaises(ValueError):
                            analyze.check_privacy(self.root, b'{}', 'synthetic-provider', 'synthetic-hash')

    def test_environment_hash_secret_and_missing_provider_refuse(self):
        target = self.root / 'analysis.json'
        with self.assertRaisesRegex(ValueError, '^Secret scan requires OPENAI_API_KEY$'):
            analyze.write_report(self.root, None)
        self.assertFalse(target.exists())
        with patch.dict(os.environ, {'TRIAL_HASH_SECRET': 'synthetic-env-hash'}):
            with patch.object(analyze, 'analyze', return_value={'untrusted': 'synthetic-env-hash'}):
                with self.assertRaises(ValueError):
                    analyze.write_report(self.root, 'synthetic-provider')
        self.assertFalse(target.exists())

    def test_scan_read_error_fails_closed(self):
        target = self.root / 'analysis.json'
        target.write_text('preserved')
        with patch.object(Path, 'read_bytes', side_effect=OSError('unreadable evidence')):
            with self.assertRaises(OSError):
                analyze.write_report(self.root, 'synthetic-provider')
        self.assertEqual(target.read_text(), 'preserved')

    def test_verification_checks_new_evidence_before_success_outputs(self):
        (self.root / 'analysis.json').write_text(json.dumps({'cases': []}))
        with patch.dict(os.environ, {'TRIAL_OUTPUT': str(self.root),
                                    'OPENAI_API_KEY': 'synthetic-provider',
                                    'TRIAL_FLEETDIFF': 'unused-mocked-binary'}):
            with patch.object(verify, 'verify_accounting', return_value={}), \
                    patch.object(verify, 'verify_fleetdiff') as fleetdiff, \
                    patch.object(verify, 'check_privacy', side_effect=ValueError('refused')) as guard, \
                    patch('sys.stdout', new=io.StringIO()) as output:
                with self.assertRaises(ValueError):
                    verify.main()
                fleetdiff.assert_called_once()
                guard.assert_called_once_with(self.root, b'{\n  "cases": []\n}', 'synthetic-provider')
                self.assertEqual(output.getvalue(), '')
        self.assertFalse((self.root / 'verification.json').exists())
        self.assertFalse((self.root / 'table.md').exists())


if __name__ == '__main__':
    unittest.main()
