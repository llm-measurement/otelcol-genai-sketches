# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Opt-in, sequential provider experiment. Offline tests never call this runner."""
import argparse
from decimal import Decimal, InvalidOperation
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import time
import uuid

SOURCE = Path(__file__).resolve().parent
REPO = SOURCE.parents[3]
MANIFEST = json.loads((SOURCE / 'manifest.json').read_text())


def spending_limit(value):
    try:
        amount = Decimal(value)
        if amount.is_finite() and amount > 0:
            return str(amount)
    except InvalidOperation:
        pass
    raise argparse.ArgumentTypeError('Use a positive finite estimated USD limit')


def run_case(root, compose, case):
    target = root / 'results' / (str(time.time_ns()) + '-' + case)
    target.mkdir(mode=0o700)
    window = MANIFEST['window_seconds']
    start = (int(time.time()) // window + 1) * window
    time.sleep(max(0, start + 1 - time.time()))
    result = subprocess.run(compose + ['exec', '-T', 'relay', 'python', '/config/client.py', case],
                            capture_output=True, timeout=180, check=False)
    (target / 'client.json').write_bytes(result.stdout)
    if result.returncode:
        (target / 'client-error.txt').write_bytes(result.stderr)
        raise RuntimeError('client process failed')
    client = json.loads(result.stdout)
    # Wait for delayed telemetry and the deliberately withheld response.
    time.sleep(18 if case.startswith('timeout') else 6)
    end = (int(time.time()) // window + 1) * window
    time.sleep(max(0, end + 3 - time.time()))
    summaries = target / 'summaries'
    summaries.mkdir(mode=0o700)
    for path in (root / 'summaries').glob('*.json'):
        value = json.loads(path.read_text())
        if start * 10**9 <= value['window_start_unix_nano'] < end * 10**9:
            (summaries / path.name).write_bytes(path.read_bytes())
    metrics = subprocess.check_output(compose + ['exec', '-T', 'relay', 'python', '-c',
        'import urllib.request; print(urllib.request.urlopen("http://collector:8889/metrics", timeout=10).read().decode())'])
    (target / 'metrics.txt').write_bytes(metrics)
    (target / 'logs.txt').write_bytes(subprocess.check_output(compose + ['logs', '--no-color']))
    (target / 'interval.json').write_text(json.dumps({'start': start, 'end': end}))
    expected = {'retry429': 429, 'timeout': 408}.get(case, 200)
    if not client['responses'] or any(r.get('status') != expected for r in client['responses']):
        raise RuntimeError('unexpected client status; suite stopped')
    if case == 'stream_complete' and not all(r.get('stream_complete') for r in client['responses']):
        raise RuntimeError('complete-stream control did not complete')


def main():
    if not __debug__:
        raise SystemExit('Run without Python optimization; reconciliation uses assertions')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run-paid-trial', action='store_true', help='allow real, billable OpenAI calls')
    parser.add_argument('--max-estimated-usd', type=spending_limit, help='required for a paid run; not a provider spend cap')
    parser.add_argument('--output', type=Path, help='new evidence directory under the repository .cache')
    parser.add_argument('--fleetdiff', default=shutil.which('fleetdiff'), help='path to the v0.2.0 binary')
    args = parser.parse_args()
    if not args.run_paid_trial or not args.max_estimated_usd:
        parser.error('Paid calls require --run-paid-trial and --max-estimated-usd')
    key = os.environ.get('OPENAI_API_KEY', '')
    if not key.startswith('sk-') or any(c.isspace() for c in key):
        parser.error('Set OPENAI_API_KEY in the environment; never pass it as an argument')
    if not args.fleetdiff:
        parser.error('Install fleetdiff v0.2.0 or set --fleetdiff')
    binary = str(Path(args.fleetdiff).resolve())
    version = subprocess.check_output([binary, '--version'], text=True).strip()
    if not version.startswith('fleetdiff v0.2.0 '):
        parser.error('This experiment pins fleetdiff v0.2.0')
    os.umask(0o077)
    run_id = 'provider-trial-' + uuid.uuid4().hex[:12]
    root = (args.output or REPO / '.cache' / run_id).resolve()
    if REPO / '.cache' not in root.parents:
        parser.error('Keep evidence under this repository\'s ignored .cache directory')
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    for name in ('evidence', 'results', 'summaries', 'fleetdiff'):
        (root / name).mkdir(mode=0o700)
    os.environ.update(TRIAL_OUTPUT=str(root), TRIAL_MAX_ESTIMATED_USD=args.max_estimated_usd,
                      TRIAL_UID=str(os.getuid()), TRIAL_GID=str(os.getgid()),
                      TRIAL_FLEETDIFF=binary)
    files = [p for p in SOURCE.iterdir() if p.suffix in ('.py', '.yaml', '.json')]
    files += [SOURCE.parent / name for name in ('provenance_callback.py', 'usage_provenance.py')]
    hashes = {str(p.relative_to(SOURCE.parent)): hashlib.sha256(p.read_bytes()).hexdigest() for p in files}
    snapshot = {'created_unix_ns': time.time_ns(), 'machine': platform.machine(),
                'python': platform.python_version(), 'files': hashes,
                'fleetdiff_version': version, 'fleetdiff_sha256': hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
                'compose_project': run_id, 'max_estimated_usd': args.max_estimated_usd,
                'manifest': MANIFEST}
    (root / 'source-hashes.json').write_text(json.dumps(snapshot, indent=2))
    compose = ['docker', 'compose', '-p', run_id, '-f', str(SOURCE / 'compose.yaml')]
    print('Private evidence:', root, flush=True)
    try:
        subprocess.run(compose + ['up', '-d', '--wait', '--wait-timeout', '120'], check=True)
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            ready = subprocess.run(compose + ['exec', '-T', 'relay', 'python', '-c',
                'import socket; socket.create_connection(("proxy",4000),2).close(); '
                'socket.create_connection(("collector",4318),2).close()'], capture_output=True)
            if ready.returncode == 0:
                break
            time.sleep(3)
        else:
            raise RuntimeError('services did not become ready; no model calls requested')
        for index, case in enumerate(MANIFEST['suite'], 1):
            print(f'Case {index}/{len(MANIFEST["suite"])}: {case}', flush=True)
            run_case(root, compose, case)
        for path in files:
            if hashlib.sha256(path.read_bytes()).hexdigest() != hashes[str(path.relative_to(SOURCE.parent))]:
                raise RuntimeError('source changed during the run')
    finally:
        # Also keep failed-run logs. Never delete the ledger to retry a case.
        logs = subprocess.run(compose + ['logs', '--no-color'], capture_output=True, check=False)
        (root / 'stack-logs.txt').write_bytes(logs.stdout + logs.stderr)
        subprocess.run(compose + ['down'], check=True)
    for script in ('analyze.py', 'verify.py'):
        subprocess.run([sys.executable, str(SOURCE / script)], check=True)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print(f'Trial stopped ({type(exc).__name__}); retain the private evidence and inspect it locally.', file=sys.stderr)
        sys.exit(1)
