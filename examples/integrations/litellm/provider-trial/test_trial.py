# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Offline tests: provider transport is always replaced, never a real API call."""
from collections import Counter
from copy import deepcopy
from decimal import Decimal
import gzip
import io
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch, Mock
import urllib.error
import urllib.request

import analyze
import client
import relay
import run
import verify


def request():
    return {'model': relay.MODEL, 'max_completion_tokens': 128, 'reasoning_effort': 'none',
            'messages': [{'role': 'user', 'content': client.SENTINEL}]}


class Boundaries(unittest.TestCase):
    def test_request_allowlist(self):
        relay.validate(request())
        for delta in ({'model': 'arbitrary'}, {'n': 2}, {'max_completion_tokens': 0},
                      {'max_completion_tokens': 2049}, {'max_completion_tokens': True},
                      {'service_tier': 'priority'}, {'reasoning_effort': 'xhigh'},
                      {'web_search_options': {}}, {'messages': []}, {'messages': [None]},
                      {'messages': [{'content': [{'type': 'image_url'}]}]},
                      {'tools': [{'type': 'function', 'function': {'name': 'execute'}}]}):
            with self.subTest(delta=delta), self.assertRaises(ValueError):
                relay.validate(dict(request(), **delta))

    def test_spending_limit(self):
        for value in ('0', '-1', 'NaN', 'Infinity', 'text'):
            with self.subTest(value=value), self.assertRaises(run.argparse.ArgumentTypeError):
                run.spending_limit(value)
        self.assertEqual(run.spending_limit('40'), '40')

    def test_account_for_unknown_work(self):
        pending = {'event': 'reserved', 'attempt': 1, 'reserve_usd': str(relay.reserve_cost(65536, 2048))}
        self.assertEqual(relay.budget_used([pending]), Decimal('0.2048'))
        done = {'event': 'provider_complete', 'attempt': 1, 'model': relay.MODEL,
                'usage': {'prompt_tokens': 1000, 'completion_tokens': 10,
                          'prompt_tokens_details': {'cached_tokens': 800}}}
        self.assertEqual(relay.budget_used([pending, done]), Decimal('0.00085'))
        self.assertEqual(relay.budget_used([pending, dict(done, usage=None)]), Decimal('0.2048'))
        self.assertIsNone(relay.usage_cost({'prompt_tokens': True, 'completion_tokens': 0}))
        self.assertIsNone(relay.usage_cost({'prompt_tokens': 10, 'completion_tokens': 0,
                                          'prompt_tokens_details': {'cached_tokens': 11}}))

    def test_no_redirect_or_retained_content(self):
        self.assertIsNone(relay.NoRedirect().redirect_request(None))
        self.assertEqual(relay.usage_only({'prompt_tokens': 5, 'secret': 'not retained',
                         'prompt_tokens_details': {'cached_tokens': 3, 'text': 'not retained'}}),
                         {'prompt_tokens': 5, 'prompt_tokens_details': {'cached_tokens': 3}})

    def test_tools_are_inert_and_requests_bounded(self):
        def send(payload):
            self.assertLessEqual(payload['max_completion_tokens'], 2048)
            if payload.get('tool_choice') != 'none':
                return {'status': 200}, {'choices': [{'message': {'role': 'assistant', 'content': None,
                    'tool_calls': [{'id': 'synthetic', 'type': 'function',
                                    'function': {'name': 'lookup_number', 'arguments': '{}'}}]}}]}
            self.assertEqual(payload['messages'][-1]['content'], '17')
            return {'status': 200}, None
        with patch.object(client, 'send', side_effect=send) as sender:
            self.assertEqual(client.run('tools')['logical_requests'], 2)
            self.assertEqual(sender.call_count, 2)

    def test_paid_run_is_explicit(self):
        with patch('sys.argv', ['run.py']), patch.object(run.subprocess, 'run') as process:
            with patch('sys.stderr', new=io.StringIO()), self.assertRaises(SystemExit):
                run.main()
            process.assert_not_called()


class RelayFaults(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.local = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        for name, value in (('ROOT', self.root), ('BUDGET', Decimal('2')), ('KEY', 'SYNTHETIC_SECRET')):
            context = patch.object(relay, name, value)
            context.start()
            self.addCleanup(context.stop)
        self.server = relay.ThreadingHTTPServer(('127.0.0.1', 0), relay.Handler)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join)
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def call(self, case, body=None, path=None, headers=None):
        data = json.dumps(body or request()).encode() if not isinstance(body, bytes) else body
        req = urllib.request.Request(f'http://127.0.0.1:{self.server.server_port}' +
                                     (path or f'/case/{case}/v1/chat/completions'), data=data, headers=headers or {})
        try:
            with self.local.open(req, timeout=5) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as exc:
            with exc:
                return exc.code, exc.read()

    def provider(self):
        return io.BytesIO(json.dumps({'model': relay.MODEL,
            'usage': {'prompt_tokens': 24, 'completion_tokens': 4},
            'choices': [{'message': {'content': 'SYNTHETIC_RESPONSE'}}]}).encode())

    def test_429_then_success_forwards_once(self):
        opener = Mock()
        opener.open.side_effect = lambda *a, **kw: self.provider()
        with patch.object(relay.urllib.request, 'build_opener', return_value=opener):
            status, _ = self.call('retry429_enabled')
            self.assertEqual(status, 429)
            self.assertEqual(self.call('retry429_enabled')[0], 200)
        self.assertEqual(opener.open.call_count, 1)
        req = opener.open.call_args.args[0]
        self.assertEqual(req.full_url, 'https://api.openai.com/v1/chat/completions')
        self.assertEqual(req.get_header('Authorization'), 'Bearer SYNTHETIC_SECRET')
        self.assertIs(json.loads(req.data)['store'], False)
        data = (self.root / 'ledger.jsonl').read_text()
        for forbidden in ('SYNTHETIC_SECRET', client.SENTINEL, 'SYNTHETIC_RESPONSE'):
            self.assertNotIn(forbidden, data)

    def test_timeout_is_after_provider_work(self):
        opener = Mock()
        opener.open.side_effect = lambda *a, **kw: self.provider()
        with patch.object(relay.urllib.request, 'build_opener', return_value=opener), patch.object(relay.time, 'sleep') as sleep:
            self.assertEqual(self.call('timeout_enabled')[0], 200)
            self.assertEqual(self.call('timeout_enabled')[0], 200)
        sleep.assert_called_once_with(14)
        events = [e for e in relay.records() if e['event'] == 'provider_complete']
        self.assertEqual([e['delivered'] for e in events], [False, True])

    def test_stream_cut_still_records_upstream_usage(self):
        chunks = [{'model': relay.MODEL, 'choices': [{'delta': {'content': 'synthetic'}}]}] * 3
        chunks.append({'model': relay.MODEL, 'usage': {'prompt_tokens': 33, 'completion_tokens': 256}})
        stream = b''.join(b'data: ' + json.dumps(c).encode() + b'\n\n' for c in chunks) + b'data: [DONE]\n\n'
        opener = Mock()
        opener.open.return_value = io.BytesIO(stream)
        with patch.object(relay.urllib.request, 'build_opener', return_value=opener):
            self.assertEqual(self.call('stream_cut', dict(request(), stream=True))[0], 200)
            # The handler drains asynchronously after closing the client socket.
            for _ in range(100):
                if any(e['event'] == 'stream_drained' for e in relay.records()):
                    break
                threading.Event().wait(0.01)
        usage = [e for e in relay.records() if e['event'] == 'provider_complete']
        self.assertEqual(len(usage), 1)
        self.assertFalse(usage[0]['delivered'])
        self.assertEqual(usage[0]['usage']['completion_tokens'], 256)

    def test_budget_and_attempt_limits_never_forward(self):
        for exhausted in ('budget', 'attempts'):
            with self.subTest(exhausted=exhausted), patch.object(relay.urllib.request, 'build_opener') as open_provider:
                if exhausted == 'budget':
                    with patch.object(relay, 'BUDGET', Decimal('0')):
                        self.assertEqual(self.call('baseline')[0], 403)
                else:
                    for i in range(relay.MANIFEST['max_provider_attempts']):
                        relay.log({'event': 'reserved', 'attempt': i + 1, 'reserve_usd': '0'})
                    self.assertEqual(self.call('baseline')[0], 403)
                open_provider.assert_not_called()

    def test_compressed_otlp_has_expanded_size_limit(self):
        status, _ = self.call('', gzip.compress(b'0' * 8_000_001), '/v1/traces', {'Content-Encoding': 'gzip'})
        self.assertEqual(status, 413)


class Reconciliation(unittest.TestCase):
    def fixture(self, root, cached=80):
        report = {'provider_attempts': 12, 'cost_plus_outstanding_reserves_usd': '0.01',
                  'privacy': {'secret_hit_files': [], 'prompt_sentinel_hit_files': []}, 'cases': []}
        running = Counter()
        for index, name in enumerate(relay.MANIFEST['suite']):
            usage = {'prompt_tokens': 100, 'completion_tokens': 10,
                     'prompt_tokens_details': {'cached_tokens': cached if name == 'cache' else 0},
                     'completion_tokens_details': {'reasoning_tokens': 3 if name == 'reasoning' else 0}}
            count = 0 if name == 'retry429' else 2 if name in ('tools', 'timeout_enabled') else 1
            events = [{'model': relay.MODEL, 'usage': deepcopy(usage),
                       'delivered': not (name.startswith('timeout') and i == 0)} for i in range(count)]
            attrs = {'gen_ai.usage.input_tokens': 100, 'gen_ai.usage.output_tokens': 10,
                     'gen_ai.usage.cache_read.input_tokens': usage['prompt_tokens_details']['cached_tokens'],
                     'gen_ai.usage.reasoning.output_tokens': usage['completion_tokens_details']['reasoning_tokens']}
            for field in ('input', 'output'):
                attrs[f'gen_ai_sketch.usage.{field}.provenance'] = 'unknown' if name.startswith('stream') else 'provider_reported'
            if name == 'stream_cut':
                attrs['gen_ai.usage.output_tokens'] = 1
            spans = [attrs]
            if name in ('timeout', 'retry429'):
                spans = [{}]
            elif name in ('retry429_enabled', 'timeout_enabled'):
                spans = [{}, attrs]
            elif name == 'tools':
                spans = [attrs, deepcopy(attrs)]
            counters = Counter(requests=len(spans), missing_token_usage=sum(not s for s in spans))
            for field in ('input', 'output'):
                counters[field + '_tokens'] = sum(s.get(f'gen_ai.usage.{field}_tokens', 0) for s in spans)
                for source in ('provider_reported', 'unknown', 'unavailable', 'inferred'):
                    counters[f'usage_provenance.v1.{field}.{source}'] = sum(
                        s.get(f'gen_ai_sketch.usage.{field}.provenance', 'unknown') == source for s in spans)
            for attribute, counter in (('cache_read.input_tokens', 'cache_read_input_tokens'),
                                       ('reasoning.output_tokens', 'reasoning_output_tokens')):
                counters[counter] = sum(s.get('gen_ai.usage.' + attribute, 0) for s in spans)
            directory = f'{index:02d}-{name}'
            report['cases'].append({'case': name, 'directory': directory, 'collector': dict(counters),
                                    'model_spans': [{'attributes': s} for s in spans],
                                    'provider_events': events, 'provider_usage': [e['usage'] for e in events],
                                    'provider_attempts': count, 'relay_received': len(spans)})
            running.update(counters)
            metrics = []
            for key, value in running.items():
                if key.startswith('usage_provenance.'):
                    _, _, field, source = key.split('.')
                    metrics.append(f'gen_ai_sketch_usage_provenance_total{{source="{source}",token_field="{field}"}} {value}')
                else:
                    metrics.append(f'gen_ai_sketch_{key}_total{{slice="by_model"}} {value}')
            target = root / 'results' / directory
            target.mkdir(parents=True)
            (target / 'metrics.txt').write_text('\n'.join(metrics) + '\n')
        return report

    def test_full_reconciliation_and_counter_mismatch(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(verify, 'BUDGET', Decimal('2')):
            root = Path(directory)
            report = self.fixture(root)
            verify.verify_accounting(report, root)
            for key in ('requests', 'input_tokens', 'missing_token_usage', 'usage_provenance.v1.input.unknown'):
                with self.subTest(key=key), self.assertRaises(AssertionError):
                    bad = deepcopy(report)
                    bad['cases'][0]['collector'][key] += 1
                    verify.verify_accounting(bad, root)
            with self.assertRaises(AssertionError):
                bad = deepcopy(report)
                bad['cases'][0]['provider_usage'][0]['prompt_tokens'] += 1
                verify.verify_accounting(bad, root)
            first = root / 'results' / report['cases'][0]['directory'] / 'metrics.txt'
            first.write_text('')
            with self.assertRaises(AssertionError):
                verify.verify_accounting(report, root)

    def test_cache_miss_is_not_a_passing_subset_check(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(verify, 'BUDGET', Decimal('2')):
            root = Path(directory)
            report = self.fixture(root, cached=0)
            with self.assertRaisesRegex(AssertionError, 'Subset was not exercised'):
                verify.verify_accounting(report, root)

    def test_privacy_scan_is_not_a_silent_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('results', 'summaries', 'fleetdiff', 'evidence'):
                (root / name).mkdir()
            (root / 'evidence/ledger.jsonl').write_text('')
            with self.assertRaises(ValueError):
                analyze.analyze(root, None)
            (root / 'fleetdiff/report.json').write_text(client.SENTINEL)
            (root / 'stack-logs.txt').write_text('SYNTHETIC_SECRET')
            report = analyze.analyze(root, 'SYNTHETIC_SECRET')
            self.assertEqual(report['privacy']['secret_hit_files'], ['stack-logs.txt'])
            self.assertEqual(report['privacy']['prompt_sentinel_hit_files'], ['fleetdiff/report.json'])

    def test_incomplete_suite_cannot_pass(self):
        with self.assertRaises(AssertionError):
            verify.verify_accounting({'cases': []}, Path('.'))

    def test_table_uses_observed_counts(self):
        report = {'cases': [{'case': 'baseline', 'provider_usage': [{'prompt_tokens': 17, 'completion_tokens': 8}],
                            'collector': {'input_tokens': 17, 'output_tokens': 8, 'requests': 1, 'missing_token_usage': 0}}]}
        table = verify.table(report)
        self.assertIn('| baseline | 17 / 8 | 17 / 8 | 1 | 0 |', table)
        self.assertIn('Model attempts', table)


if __name__ == '__main__':
    unittest.main()
