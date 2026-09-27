# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Compare provider evidence, emitted attributes, and exported counters."""
from collections import Counter
import json
from pathlib import Path
import os
from relay import RUN_ID, PRIOR_COST, budget_used, usage_cost
from client import SENTINEL

def analyze(root, secret):
    ledger = [json.loads(line) for line in (root / 'evidence/ledger.jsonl').read_text().splitlines()]
    ledger = [e for e in ledger if e.get('run_id') == RUN_ID]
    price = sum(usage_cost(e['usage']) for e in ledger if e.get('usage'))

    report = {'provider_attempts': sum(e['event'] == 'reserved' for e in ledger),
              'prior_reconciled_cost_usd': str(PRIOR_COST),
              'cost_plus_outstanding_reserves_usd': str(budget_used(ledger)),
              'observed_usage_estimated_usd': str(price), 'cases': []}
    for directory in sorted((root / 'results').iterdir()):
        if not (directory / 'interval.json').exists():
            continue
        interval = json.loads((directory / 'interval.json').read_text())
        client = json.loads((directory / 'client.json').read_text())
        start, end = interval['start'] * 10**9, interval['end'] * 10**9
        events = [e for e in ledger if start <= e['time_ns'] < end]
        counters = Counter()
        for path in (directory / 'summaries').glob('*.json'):
            summary = json.loads(path.read_text())
            assert summary['observed_start_unix_nano'] == summary['window_start_unix_nano']
            assert summary['observed_end_unix_nano'] == summary['window_start_unix_nano'] + summary['window_duration_unix_nano']
            counters.update(summary['counters'])
        spans = []
        for path in (root / 'evidence').glob('otlp-*.json'):
            if not start <= int(path.stem.removeprefix('otlp-')) < end:
                continue
            for resource in json.loads(path.read_text()).get('resourceSpans', []):
                for scope in resource.get('scopeSpans', []):
                    for span in scope.get('spans', []):
                        attrs = {a['key']: next(iter(a['value'].values())) for a in span.get('attributes', [])}
                        if 'gen_ai.request.model' in attrs or 'gen_ai.operation.name' in attrs:
                            spans.append({'name': span['name'], 'status': span.get('status'),
                                          'attributes': {k: v for k, v in attrs.items() if k.startswith(('gen_ai.', 'gen_ai_sketch.'))}})
        selected = {k: v for k, v in counters.items() if k in ('requests', 'input_tokens', 'output_tokens',
                    'missing_token_usage', 'cache_read_input_tokens', 'reasoning_output_tokens') or k.startswith('usage_provenance.')}
        provider_events = [e for e in events if e.get('usage')]
        provider = [e['usage'] for e in provider_events]
        report['cases'].append({'directory': directory.name, 'case': client['case'], 'client': client,
                               'relay_received': sum(e['event'] == 'received' for e in events),
                               'provider_attempts': sum(e['event'] == 'reserved' for e in events),
                               'provider_usage': provider, 'provider_events': provider_events,
                               'collector': selected, 'model_spans': spans})

    # Only print counts and paths on failure; never print secrets or captured values.
    if not secret:
        raise ValueError('Secret scan requires OPENAI_API_KEY')
    secret = secret.encode()
    sentinel = SENTINEL.encode()
    secret_hits, sentinel_hits = [], []
    for path in (list((root / 'evidence').rglob('*')) + list((root / 'results').rglob('*'))
                 + list((root / 'summaries').rglob('*')) + list((root / 'fleetdiff').rglob('*')) + list(root.glob('*.txt'))):
        if path.is_file():
            data = path.read_bytes()
            if secret in data:
                secret_hits.append(str(path.relative_to(root)))
            if sentinel in data:
                sentinel_hits.append(str(path.relative_to(root)))
    report['privacy'] = {'secret_hit_files': secret_hits, 'prompt_sentinel_hit_files': sentinel_hits}
    return report


def main():
    root = Path(os.environ['TRIAL_OUTPUT'])
    report = analyze(root, os.environ.get('OPENAI_API_KEY'))
    (root / 'analysis.json').write_text(json.dumps(report, indent=2))
    if report['privacy']['secret_hit_files'] or report['privacy']['prompt_sentinel_hit_files']:
        raise SystemExit('Privacy scan failed; keep evidence private and inspect locally')
    print(json.dumps({'provider_attempts': report['provider_attempts'],
                      'observed_usage_estimated_usd': report['observed_usage_estimated_usd'],
                      'completed_cases': len(report['cases']), 'privacy': 'pass'}))


if __name__ == '__main__':
    main()
