"""Pure token-usage projections shared by UI and reporting adapters."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any


def _non_negative(value: Any) -> int:
    try:
        return max(0, int(value))
    except (TypeError, ValueError, OverflowError):
        return 0


def token_breakdown(value: Mapping[str, Any]) -> dict[str, int]:
    """Return additive and subset counters without counting cache/reasoning twice."""
    input_tokens = _non_negative(value.get("input_tokens"))
    context_tokens = max(input_tokens, _non_negative(value.get("context_tokens")))
    raw_cached_tokens = _non_negative(value.get("cached_tokens"))
    cached_tokens = (
        min(context_tokens, raw_cached_tokens) if context_tokens else raw_cached_tokens
    )
    output_tokens = _non_negative(value.get("output_tokens"))
    reasoning_tokens = min(
        output_tokens, _non_negative(value.get("reasoning_tokens"))
    )
    processed_tokens = max(
        context_tokens + output_tokens,
        _non_negative(value.get("total_tokens")),
    )
    fresh_input_tokens = max(0, context_tokens - cached_tokens)
    return {
        "input_tokens": input_tokens,
        "context_tokens": context_tokens,
        "fresh_input_tokens": fresh_input_tokens,
        "output_tokens": output_tokens,
        "cached_tokens": cached_tokens,
        "reasoning_tokens": reasoning_tokens,
        "total_tokens": processed_tokens,
        "non_cached_tokens": max(0, processed_tokens - cached_tokens),
    }
