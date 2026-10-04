"""Settings read from environment variables (same names as the Go services)."""

from __future__ import annotations

import os
import re

_DURATION = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h|d)")
_UNITS = {"ms": 0.001, "s": 1, "m": 60, "h": 3600, "d": 86400}


def env_str(key: str, default: str) -> str:
    return os.environ.get(key) or default


def env_duration(key: str, default_seconds: float) -> float:
    """Parse values like "15m", "24h" or "1h30m" into seconds."""
    raw = os.environ.get(key, "")
    parts = _DURATION.findall(raw)
    if not parts or "".join(n + u for n, u in parts) != raw:
        return default_seconds
    return sum(float(n) * _UNITS[u] for n, u in parts)
