# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Capture OpenAI-compatible usage before normalization, without retaining content.

Call capture() on a decoded raw provider usage object (or None when absent),
then attributes() with the emitted counts. Only an instrumented estimator may
declare inferred_fields. provenance_callback.py connects the raw-response hook.
"""
from dataclasses import dataclass

FIELDS = (('input', 'prompt_tokens'), ('output', 'completion_tokens'))
PREFIX = 'gen_ai_sketch.usage.'


def valid_count(value):
    return type(value) is int and 0 <= value <= (1 << 63) - 1


@dataclass(frozen=True)
class ProviderUsage:
    input: int | None
    output: int | None

    def __post_init__(self):
        if any(v is not None and not valid_count(v) for v in (self.input, self.output)):
            raise ValueError('invalid captured provider count')

    def attributes(self, emitted_usage, *, inferred_fields=()):
        inferred = frozenset(inferred_fields)
        if not inferred <= {'input', 'output'}:
            raise ValueError('unsupported inferred usage field')
        emitted = emitted_usage if isinstance(emitted_usage, dict) else {}
        result = {}
        for field, key in FIELDS:
            original, value = getattr(self, field), emitted.get(key)
            if original is not None:
                # A changed provider count has lost its original provenance.
                source = 'provider_reported' if valid_count(value) and value == original else 'unknown'
            elif field in inferred and valid_count(value):
                source = 'inferred'
            else:
                source = 'unavailable'
            result[PREFIX + field + '.provenance'] = source
        return result


def capture(raw_usage):
    """None means source usage was inspected and absent, not unobserved."""
    usage = raw_usage if isinstance(raw_usage, dict) else {}
    return ProviderUsage(*(v if valid_count(v := usage.get(key)) else None for _, key in FIELDS))
