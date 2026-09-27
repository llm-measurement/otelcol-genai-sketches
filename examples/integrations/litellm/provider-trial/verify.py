# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Assert observed accounting, while explicitly reporting unsupported subsets."""
import json
import os
from collections import Counter
from pathlib import Path
import re
import subprocess
from decimal import Decimal
from relay import BUDGET, MODEL, MANIFEST
from analyze import analyze

def verify_accounting(report, root):
    assert [c['case'] for c in report['cases']] == MANIFEST['suite'], 'Incomplete or reordered suite'
    assert report['provider_attempts'] == MANIFEST['max_provider_attempts']
    assert Decimal(report['cost_plus_outstanding_reserves_usd']) <= BUDGET
    assert not report['privacy']['secret_hit_files']
    assert not report['privacy']['prompt_sentinel_hit_files']
    cases = {c['case']: c for c in report['cases']}
    running = Counter()
    for case in report['cases']:
        counters = case['collector']
        spans = case['model_spans']
        assert len(case['provider_events']) == case['provider_attempts'], 'Every billed attempt needs independent usage evidence'
        assert all(e['model'] == MODEL for e in case['provider_events']), 'Unexpected provider model'
        assert counters['requests'] == len(spans), case['case']
        if case['case'] in ('baseline', 'stream_complete', 'cache', 'reasoning', 'tools'):
            for provider_field, counter in (('prompt_tokens', 'input_tokens'), ('completion_tokens', 'output_tokens')):
                assert counters[counter] == sum(u[provider_field] for u in case['provider_usage'])
        for field in ('input', 'output'):
            observed = sum(int(s['attributes'].get(f'gen_ai.usage.{field}_tokens', 0)) for s in spans
                           if s['attributes'].get(f'gen_ai_sketch.usage.{field}.provenance') != 'unavailable')
            assert counters[field + '_tokens'] == observed, (case['case'], field)
            for source in ('provider_reported', 'unknown', 'unavailable', 'inferred'):
                declared = sum(s['attributes'].get(f'gen_ai_sketch.usage.{field}.provenance', 'unknown') == source for s in spans)
                assert counters[f'usage_provenance.v1.{field}.{source}'] == declared
        missing = sum(any(f'gen_ai.usage.{field}_tokens' not in s['attributes'] or
                          s['attributes'].get(f'gen_ai_sketch.usage.{field}.provenance') == 'unavailable'
                          for field in ('input', 'output')) for s in spans)
        assert counters['missing_token_usage'] == missing
        for attribute, counter in (('gen_ai.usage.cache_read.input_tokens', 'cache_read_input_tokens'),
                                   ('gen_ai.usage.reasoning.output_tokens', 'reasoning_output_tokens')):
            assert counters[counter] == sum(int(s['attributes'].get(attribute, 0)) for s in spans)
        running.update(counters)
        metrics = (root / 'results' / case['directory'] / 'metrics.txt').read_text()
        for counter in ('requests', 'input_tokens', 'output_tokens', 'missing_token_usage',
                        'cache_read_input_tokens', 'reasoning_output_tokens'):
            values = re.findall(r'^gen_ai_sketch_' + counter + r'_total\{[^\n]*\} ([0-9.eE+-]+)$', metrics, re.M)
            assert sum(Decimal(v) for v in values) == running[counter], (case['case'], 'metric', counter)
        provenance = re.findall(r'^gen_ai_sketch_usage_provenance_total\{([^\n]+)\} ([0-9.eE+-]+)$', metrics, re.M)
        for field in ('input', 'output'):
            for source in ('provider_reported', 'unknown', 'unavailable', 'inferred'):
                observed = sum(Decimal(value) for labels, value in provenance
                               if f'token_field="{field}"' in labels and f'source="{source}"' in labels)
                assert observed == running[f'usage_provenance.v1.{field}.{source}']

    assert cases['tools']['collector']['requests'] == 2
    assert cases['stream_cut']['collector']['usage_provenance.v1.output.unknown'] == 1
    assert cases['stream_cut']['provider_usage'][0]['completion_tokens'] > cases['stream_cut']['collector']['output_tokens']
    assert cases['timeout']['collector']['missing_token_usage'] == 1

    for name, provider_calls in (('retry429_enabled', 1), ('timeout_enabled', 2)):
        case = cases[name]
        assert case['relay_received'] == 2 and case['provider_attempts'] == provider_calls
        assert case['collector']['requests'] == 2
        assert case['collector']['missing_token_usage'] == 1
        delivered = [e['usage'] for e in case['provider_events'] if e['delivered']]
        assert len(delivered) == 1
        assert case['collector']['input_tokens'] == delivered[0]['prompt_tokens']
        assert case['collector']['output_tokens'] == delivered[0]['completion_tokens']
        assert len(case['provider_usage']) == provider_calls
        assert case['collector']['usage_provenance.v1.input.provider_reported'] == 1
        assert case['collector']['usage_provenance.v1.input.unknown'] == 1

    for name, field, details, source in (
            ('cache', 'cache_read_input_tokens', 'prompt_tokens_details', 'cached_tokens'),
            ('reasoning', 'reasoning_output_tokens', 'completion_tokens_details', 'reasoning_tokens')):
        case = cases[name]
        value = sum((u.get(details) or {}).get(source, 0) for u in case['provider_usage'])
        assert value > 0, 'Subset was not exercised'
        assert case['collector'][field] == value, (name, field, value, case['collector'][field])
        raw_field = 'prompt_tokens' if name == 'cache' else 'completion_tokens'
        total_field = 'input_tokens' if name == 'cache' else 'output_tokens'
        assert case['collector'][total_field] == sum(u[raw_field] for u in case['provider_usage'])

    return cases


def verify_fleetdiff(cases, root, binary):
    prepared = root / 'fleetdiff'
    prepared.mkdir(exist_ok=True)
    for name in ('baseline', 'tools', 'timeout', 'stream_cut', 'cache', 'reasoning'):
        case = cases[name]
        paths = list((root / 'results' / case['directory'] / 'summaries').glob('*.json'))
        active = [p for p in paths if json.loads(p.read_text())['counters']['requests'] > 0]
        assert len(active) == 1, 'Do not rewrite or combine different windows for comparison'
        directory = prepared / name
        directory.mkdir(exist_ok=True)
        (directory / 'app.json').write_bytes(active[0].read_bytes())
    for name in ('tools', 'timeout', 'stream_cut', 'cache', 'reasoning'):
        raw = subprocess.check_output([str(binary), 'investigate', '--before', str(prepared / 'baseline'),
            '--after', str(prepared / name), '--expected', 'app', '--format', 'json'])
        (prepared / (name + '.json')).write_bytes(raw)
        questions = {q['id']: q for q in json.loads(raw)['questions']}
        assert questions['volume']['status'] == ('cannot_determine' if name == 'timeout' else 'observed')
        assert questions['usage_source']['status'] == ('cannot_determine' if name in ('timeout', 'stream_cut') else 'observed')
        if name in ('cache', 'reasoning'):
            report_counters = {c['name']: c['after'] for c in json.loads(raw)['evidence']['counters']}
            subset = 'cache_read_input_tokens' if name == 'cache' else 'reasoning_output_tokens'
            assert report_counters[subset] == cases[name]['collector'][subset] > 0


def table(report):
    lines = ['| Case | Provider input / output | Collector input / output | Model attempts | Missing |',
             '|---|---|---|---:|---:|']
    for case in report['cases']:
        inp = sum(u['prompt_tokens'] for u in case['provider_usage'])
        out = sum(u['completion_tokens'] for u in case['provider_usage'])
        provider = f'{inp} / {out}' if case['provider_usage'] else 'none'
        c = case['collector']
        lines.append(f"| {case['case']} | {provider} | {c['input_tokens']} / {c['output_tokens']} | {c['requests']} | {c['missing_token_usage']} |")
    return '\n'.join(lines) + '\n'


def main():
    if not __debug__:
        raise SystemExit('Run without Python optimization; reconciliation uses assertions')
    root = Path(os.environ['TRIAL_OUTPUT'])
    report = json.loads((root / 'analysis.json').read_text())
    cases = verify_accounting(report, root)
    verify_fleetdiff(cases, root, os.environ['TRIAL_FLEETDIFF'])
    # Include the newly generated Fleetdiff reports before marking success.
    scanned = analyze(root, os.environ.get('OPENAI_API_KEY'))
    assert not scanned['privacy']['secret_hit_files']
    assert not scanned['privacy']['prompt_sentinel_hit_files']
    (root / 'table.md').write_text(table(report))
    (root / 'verification.json').write_text(json.dumps({'accounting': 'pass', 'fleetdiff': 'pass'}))
    print('Accounting, metric, provenance, subset, and Fleetdiff checks passed.')


if __name__ == '__main__':
    main()
