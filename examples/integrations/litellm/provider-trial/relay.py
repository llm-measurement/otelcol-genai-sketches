# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Opt-in synthetic trial relay. Credentials are only sent to api.openai.com."""
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import socket
import threading
import time
import urllib.error
import urllib.request
from decimal import Decimal
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path('/evidence')
LOCK = threading.Lock()
MANIFEST = json.loads(Path(__file__).with_name('manifest.json').read_text())
MODEL = MANIFEST['model']
MODELS = {MODEL}
CASES = {'baseline', 'retry429', 'timeout', 'stream_complete', 'stream_cut', 'cache', 'reasoning', 'tools',
         'retry429_enabled', 'timeout_enabled'}
MAX_INPUT_BYTES = MANIFEST['max_request_bytes']
MAX_OUTPUT_TOKENS = MANIFEST['max_completion_tokens']
BUDGET = Decimal(os.environ.get('TRIAL_MAX_ESTIMATED_USD', '0'))
PRIOR_COST = Decimal('0')
RATES = {k: Decimal(v) for k, v in MANIFEST['rates_usd_per_million'].items()}
KEY = None
RUN_ID = MANIFEST['run_id']


def usage_cost(usage):
    if not isinstance(usage, dict):
        return None
    inp, out = usage.get('prompt_tokens'), usage.get('completion_tokens')
    cached = (usage.get('prompt_tokens_details') or {}).get('cached_tokens', 0)
    if any(type(v) is not int or v < 0 for v in (inp, out, cached)) or cached > inp:
        return None
    return ((inp - cached) * RATES['input'] + cached * RATES['cached_input'] + out * RATES['output']) / 1_000_000


def reserve_cost(input_bytes, output_tokens):
    # Text-only JSON byte length plus generous wrapper allowance bounds this
    # fixed request shape. No cached discount is assumed before the response.
    return ((input_bytes + 4096) * RATES['input'] + output_tokens * RATES['output']) / 1_000_000


def budget_used(history):
    complete = {r['attempt']: r for r in history if r.get('event') == 'provider_complete'}
    total = PRIOR_COST
    for event in history:
        if event.get('event') != 'reserved':
            continue
        result = complete.get(event['attempt'], {})
        actual = usage_cost(result.get('usage')) if result.get('model') == MODEL else None
        total += actual if actual is not None else Decimal(event['reserve_usd'])
    return total


def records():
    path = ROOT / 'ledger.jsonl'
    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []


def log(record):
    with LOCK:
        with (ROOT / 'ledger.jsonl').open('a') as out:
            out.write(json.dumps(dict(time_ns=time.time_ns(), run_id=RUN_ID, **record)) + '\n')
            out.flush()
            os.fsync(out.fileno())


def usage_only(value):
    if not isinstance(value, dict):
        return None
    allowed = {'prompt_tokens', 'completion_tokens', 'total_tokens', 'prompt_tokens_details',
               'completion_tokens_details', 'cached_tokens', 'reasoning_tokens', 'audio_tokens',
               'accepted_prediction_tokens', 'rejected_prediction_tokens'}
    return {k: usage_only(v) if isinstance(v, dict) else v for k, v in value.items()
            if k in allowed and (isinstance(v, dict) or type(v) is int)}


def validate(body):
    if not isinstance(body, dict):
        raise ValueError('request must be an object')
    allowed = {'model', 'messages', 'n', 'max_completion_tokens', 'reasoning_effort',
               'service_tier', 'stream', 'stream_options', 'tools', 'tool_choice',
               'parallel_tool_calls', 'store'}
    if set(body) - allowed:
        raise ValueError('unsupported request field')
    if body.get('model') not in MODELS or body.get('n', 1) != 1:
        raise ValueError('model or multiplicity not permitted')
    if type(body.get('max_completion_tokens')) is not int or body['max_completion_tokens'] not in range(1, MAX_OUTPUT_TOKENS + 1):
        raise ValueError('explicit bounded completion required')
    if body.get('service_tier', 'default') != 'default' or body.get('reasoning_effort') not in ('none', 'low'):
        raise ValueError('explicit reviewed tier and reasoning setting required')
    if not isinstance(body.get('messages'), list) or not body['messages']:
        raise ValueError('messages required')
    for message in body['messages']:
        if not isinstance(message, dict):
            raise ValueError('invalid message')
        if message.get('content') is not None and not isinstance(message['content'], str):
            raise ValueError('text only')
    if not isinstance(body.get('tools', []), list):
        raise ValueError('invalid tools')
    for tool in body.get('tools', []):
        if not isinstance(tool, dict) or tool.get('type') != 'function' or tool.get('function', {}).get('name') != 'lookup_number':
            raise ValueError('only inert local test tool allowed')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, status, body, content_type='application/json'):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        try:
            self.handle_post()
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as exc:
            log({'event': 'handler_error', 'type': type(exc).__name__})
            try:
                self.reply(502, b'{"error":{"message":"trial relay error","type":"api_error"}}')
            except OSError:
                pass

    def handle_post(self):
        self.connection.settimeout(90)
        size = int(self.headers.get('Content-Length', '0'))
        if self.path == '/v1/traces':
            if not 0 < size <= 4_000_000:
                return self.reply(413, b'{}')
            data = self.rfile.read(size)
            if self.headers.get('Content-Encoding') == 'gzip':
                with gzip.GzipFile(fileobj=io.BytesIO(data)) as compressed:
                    data = compressed.read(8_000_001)
            if len(data) > 8_000_000:
                return self.reply(413, b'{}')
            from google.protobuf.json_format import MessageToDict
            from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import ExportTraceServiceRequest
            message = ExportTraceServiceRequest.FromString(data)
            (ROOT / f'otlp-{time.time_ns()}.json').write_text(json.dumps(MessageToDict(message)))
            req = urllib.request.Request('http://collector:4318/v1/traces', data=data,
                                         headers={'Content-Type': 'application/x-protobuf'})
            with urllib.request.urlopen(req, timeout=10) as response:
                self.reply(response.status, response.read(), 'application/x-protobuf')
            return
        match = re.fullmatch(r'/case/([a-z0-9_]+)/v1/chat/completions', self.path)
        if not match or match[1] not in CASES or not 0 < size <= MAX_INPUT_BYTES:
            return self.reply(400, b'{}')
        case = match[1]
        data = self.rfile.read(size)
        body = json.loads(data)
        validate(body)
        # This setting does not change the accounting experiment.
        body['store'] = False
        data = json.dumps(body).encode()
        with LOCK:
            history = records()
            prior = sum(r.get('event') == 'received' and r.get('case') == case and r.get('run_id') == RUN_ID for r in history)
            with (ROOT / 'ledger.jsonl').open('a') as out:
                out.write(json.dumps({'time_ns': time.time_ns(), 'event': 'received', 'case': case, 'run_id': RUN_ID}) + '\n')
        if case.startswith('retry429') and prior == 0:
            log({'event': 'injected_429', 'case': case, 'forwarded': False})
            self.send_response(429)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Retry-After', '1')
            self.end_headers()
            self.wfile.write(b'{"error":{"message":"controlled trial retry","type":"rate_limit_error","code":"rate_limit_exceeded"}}')
            return
        # Account for known completions at observed usage; keep the full reserve
        # for unknown work. Rates are recorded estimates, not a billing cap.
        with LOCK:
            history = records()
            reserve = reserve_cost(size, body['max_completion_tokens'])
            if not BUDGET.is_finite() or BUDGET <= 0 or budget_used(history) + reserve > BUDGET:
                return self.reply(403, b'{"error":{"message":"authorized trial budget reached"}}')
            attempt = sum(r.get('event') == 'reserved' for r in history) + 1
            if attempt > MANIFEST['max_provider_attempts']:
                return self.reply(403, b'{"error":{"message":"trial attempt limit reached"}}')
            with (ROOT / 'ledger.jsonl').open('a') as out:
                out.write(json.dumps({'time_ns': time.time_ns(), 'event': 'reserved', 'case': case, 'run_id': RUN_ID,
                                      'attempt': attempt, 'reserve_usd': str(reserve), 'model': body['model'],
                                      'request_sha256': hashlib.sha256(data).hexdigest(),
                                      'reasoning_effort': body['reasoning_effort'], 'service_tier': body.get('service_tier', 'default'),
                                      'input_bytes': size, 'max_completion_tokens': body['max_completion_tokens']}) + '\n')
                out.flush()
                os.fsync(out.fileno())
        req = urllib.request.Request('https://api.openai.com/v1/chat/completions', data=data,
                                     headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + KEY})
        try:
            response = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect()).open(req, timeout=70)
        except urllib.error.HTTPError as exc:
            error = {}
            try:
                with exc:
                    error = json.loads(exc.read(65536)).get('error') or {}
            except (ValueError, AttributeError):
                pass
            code = error.get('code') if isinstance(error, dict) else None
            safe_code = code if code in ('model_not_found', 'unsupported_parameter', 'unsupported_value',
                'invalid_api_key', 'insufficient_quota', 'rate_limit_exceeded', 'context_length_exceeded') else 'unclassified'
            log({'event': 'provider_error', 'attempt': attempt, 'case': case, 'status': exc.code, 'code': safe_code})
            # Do not forward provider error text, which might contain identifiers.
            return self.reply(exc.code, b'{"error":{"message":"provider rejected trial request","type":"api_error"}}')
        except Exception as exc:
            log({'event': 'provider_transport_error', 'attempt': attempt, 'case': case, 'type': type(exc).__name__})
            return self.reply(502, b'{"error":{"message":"provider transport failure"}}')
        with response:
            if not body.get('stream'):
                result = response.read(2_000_001)
                if len(result) > 2_000_000:
                    raise ValueError('response too large')
                decoded = json.loads(result)
                log({'event': 'provider_complete', 'attempt': attempt, 'case': case,
                     'usage': usage_only(decoded.get('usage')),
                     'model': decoded.get('model'), 'delivered': not (case.startswith('timeout') and prior == 0)})
                if case.startswith('timeout') and prior == 0:
                    log({'event': 'injected_timeout_after_provider_completion', 'case': case, 'attempt': attempt})
                    time.sleep(14)
                self.reply(200, result)
                return
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            cut = False
            chunks = 0
            received_bytes = 0
            while True:
                line = response.readline(1_000_001)
                if not line:
                    break
                received_bytes += len(line)
                if len(line) > 1_000_000 or received_bytes > 4_000_000:
                    raise ValueError('stream line too large')
                if line.startswith(b'data: ') and line.strip() != b'data: [DONE]':
                    decoded = json.loads(line[6:])
                    chunks += 1
                    if decoded.get('usage'):
                        log({'event': 'provider_complete', 'attempt': attempt, 'case': case,
                             'usage': usage_only(decoded['usage']), 'model': decoded.get('model'), 'delivered': not cut})
                if case == 'stream_cut' and chunks >= 3 and not cut:
                    cut = True
                    log({'event': 'injected_stream_disconnect', 'case': case, 'attempt': attempt})
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                if not cut:
                    try:
                        self.wfile.write(line)
                        self.wfile.flush()
                    except (BrokenPipeError, ConnectionResetError):
                        cut = True
                        log({'event': 'client_disconnected', 'case': case, 'attempt': attempt})
            log({'event': 'stream_drained', 'case': case, 'attempt': attempt, 'chunks': chunks})


if __name__ == '__main__':
    os.umask(0o077)
    KEY = os.environ.pop('OPENAI_API_KEY', '')
    if not re.fullmatch(r'sk-[A-Za-z0-9_-]{30,}', KEY):
        raise SystemExit('Set OPENAI_API_KEY; nothing was sent')
    if not BUDGET.is_finite() or BUDGET <= 0:
        raise SystemExit('A positive finite estimated-spend limit is required')
    print('Synthetic trial relay ready; credentials loaded without display', flush=True)
    ThreadingHTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
