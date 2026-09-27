# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Synthetic client; never loads credentials or prints provider content."""
import json
import sys
import urllib.error
import urllib.request

SENTINEL = 'PRIVATE_OPENAI_TRIAL_SENTINEL_20260927'


def send(payload):
    req = urllib.request.Request('http://proxy:4000/v1/chat/completions',
        data=json.dumps(payload).encode(), headers={'Content-Type': 'application/json',
        'Authorization': 'Bearer sk-local-trial-only'})
    try:
        with urllib.request.urlopen(req, timeout=100) as response:
            if payload.get('stream'):
                chunks, usage, done = 0, None, False
                try:
                    for line in response:
                        if not line.startswith(b'data: '):
                            continue
                        if line.strip() == b'data: [DONE]':
                            done = True
                            continue
                        chunk = json.loads(line[6:])
                        chunks += 1
                        if chunk.get('usage'):
                            usage = chunk['usage']
                except Exception as exc:
                    return {'status': 200, 'stream_complete': False, 'error_type': type(exc).__name__,
                            'chunks': chunks, 'usage': usage}, None
                return {'status': 200, 'stream_complete': done, 'chunks': chunks, 'usage': usage}, None
            body = json.loads(response.read(2_000_000))
            return {'status': response.status, 'usage': body.get('usage'),
                    'finish_reason': body['choices'][0].get('finish_reason')}, body
    except urllib.error.HTTPError as exc:
        with exc:
            return {'status': exc.code, 'error_type': 'HTTPError'}, None
    except Exception as exc:
        return {'status': None, 'error_type': type(exc).__name__}, None


def run(case):
    payload = {'model': case, 'max_completion_tokens': 128, 'reasoning_effort': 'none',
               'service_tier': 'default', 'messages': [
        {'role': 'user', 'content': SENTINEL + '. Reply only with OK.'}]}
    if case in ('stream_complete', 'stream_cut'):
        payload.update(stream=True, stream_options={'include_usage': True}, max_completion_tokens=256)
        payload['messages'][0]['content'] = SENTINEL + '. List the integers from 1 to 150, separated by spaces.'
    if case == 'cache':
        prefix = '\n'.join(f'Record {i}: synthetic warehouse batch, category green, status ready, count 17.' for i in range(180))
        payload['messages'] = [{'role': 'system', 'content': SENTINEL + '\n' + prefix},
                               {'role': 'user', 'content': 'Reply only with OK.'}]
    if case == 'reasoning':
        payload.update(max_completion_tokens=2048, reasoning_effort='low')
        payload['messages'][0]['content'] = SENTINEL + '. Find the smallest positive integer divisible by 12, 18 and 25. Give only the number.'
    results = []
    if case == 'tools':
        function = {'type': 'function', 'function': {'name': 'lookup_number',
            'description': 'Return a fixed synthetic test number.', 'parameters': {
                'type': 'object', 'properties': {}, 'additionalProperties': False}}}
        payload['tools'] = [function]
        payload['tool_choice'] = {'type': 'function', 'function': {'name': 'lookup_number'}}
        payload['parallel_tool_calls'] = False
        result, body = send(payload)
        results.append(result)
        if body and body['choices'][0]['message'].get('tool_calls'):
            message = body['choices'][0]['message']
            calls = message['tool_calls']
            if len(calls) != 1 or calls[0]['function']['name'] != 'lookup_number':
                raise SystemExit('Unexpected tool request; no tool executed')
            payload['messages'].extend([message, {'role': 'tool', 'tool_call_id': calls[0]['id'], 'content': '17'}])
            payload['tool_choice'] = 'none'
            result, _ = send(payload)
            results.append(result)
    else:
        result, _ = send(payload)
        results.append(result)
    return {'case': case, 'logical_requests': len(results), 'responses': results}


if __name__ == '__main__':
    print(json.dumps(run(sys.argv[1])))
