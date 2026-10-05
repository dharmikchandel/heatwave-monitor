"""One JSON object per line on stdout, the same shape the Go services log."""

from __future__ import annotations

import json
import sys
import threading
from datetime import datetime, timezone

SERVICE = "prediction"
_lock = threading.Lock()


def log(level: str, msg: str, **fields) -> None:
    record = {"time": datetime.now(timezone.utc).isoformat(), "level": level.upper(), "msg": msg, "service": SERVICE, **fields}
    with _lock:
        sys.stdout.write(json.dumps(record, default=str) + "\n")
        sys.stdout.flush()
