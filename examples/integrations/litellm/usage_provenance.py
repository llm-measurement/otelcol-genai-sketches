# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Capture OpenAI-compatible usage before normalization, without retaining content.

Call capture() on a decoded raw provider usage object (or None when absent),
then attributes() with the emitted counts and subset_attributes() for validated
subsets. Only an instrumented estimator may declare inferred_fields.
provenance_callback.py connects the raw-response hook.
"""
from dataclasses import dataclass

FIELDS = (('input', 'prompt_tokens'), ('output', 'completion_tokens'))
PREFIX = 'gen_ai_sketch.usage.'
SUBSETS = (
    ('input', 'cache_read_input', 'prompt_tokens_details', 'cached_tokens', 'gen_ai.usage.cache_read.input_tokens'),
    ('output', 'reasoning_output', 'completion_tokens_details', 'reasoning_tokens', 'gen_ai.usage.reasoning.output_tokens'),
)


def valid_count(value):
    return type(value) is int and 0 <= value <= (1 << 63) - 1


@dataclass(frozen=True)
class ProviderUsage:
    input: int | None
    output: int | None
    cache_read_input: int | None = None
    reasoning_output: int | None = None

    def __post_init__(self):
        if any(v is not None and not valid_count(v) for v in (self.input, self.output)):
            raise ValueError('invalid captured provider count')
        for field, name, _, _, _ in SUBSETS:
            parent, value = getattr(self, field), getattr(self, name)
            if value is not None and (not valid_count(value) or parent is None or value > parent):
                raise ValueError('invalid captured provider subset')

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

    def subset_attributes(self, emitted_usage):
        """Emit source subsets only when totals still match; never infer from cost."""
        provenance = self.attributes(emitted_usage)
        emitted = emitted_usage if isinstance(emitted_usage, dict) else {}
        result = {}
        for field, name, details, key, attribute in SUBSETS:
            value = getattr(self, name)
            if value is None or provenance[PREFIX + field + '.provenance'] != 'provider_reported':
                continue
            normalized = emitted.get(details)
            if normalized is not None:
                if not isinstance(normalized, dict):
                    continue
                if key in normalized and (not valid_count(normalized[key]) or normalized[key] != value):
                    continue
            result[attribute] = value
        return result


def capture(raw_usage):
    """None means source usage was inspected and absent, not unobserved."""
    usage = raw_usage if isinstance(raw_usage, dict) else {}
    totals = {field: v if valid_count(v := usage.get(key)) else None for field, key in FIELDS}
    subsets = {}
    for field, name, details, key, _ in SUBSETS:
        data = usage.get(details)
        value = data.get(key) if isinstance(data, dict) else None
        parent = totals[field]
        subsets[name] = value if valid_count(value) and parent is not None and value <= parent else None
    return ProviderUsage(**totals, **subsets)
