"""The event envelope and the inbox/outbox that move events between services.

Same wire format and delivery guarantees as the Go services: at-least-once,
in order per target, deduplicated by event ID on the receiving side.
"""

from __future__ import annotations

import json
import os
import sqlite3
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from typing import Any, Callable

from .db import Database
from .logs import log


def new_id() -> str:
    """Roughly time-sortable unique ID: 12 hex chars of unix ms + 16 random hex chars."""
    return f"{int(time.time() * 1000):012x}{os.urandom(8).hex()}"


def validate_envelope(ev: Any) -> str | None:
    """Return a problem description, or None if ``ev`` is a usable event envelope."""
    if not isinstance(ev, dict):
        return "event must be a JSON object"
    for key in ("id", "type"):
        if not isinstance(ev.get(key), str) or not ev[key]:
            return f"missing {key}"
    if not isinstance(ev.get("payload"), (dict, list)):
        return "missing payload"
    return None


# ---- inbox ----

def receive(db: Database, ev: dict, handle: Callable[[sqlite3.Connection, dict], None]) -> bool:
    """Process ``ev`` exactly once. Returns False if it was already handled.

    The dedupe record and everything ``handle`` writes commit together; if
    ``handle`` raises, all of it rolls back so the producer's retry reprocesses it.
    """
    with db.transaction() as tx:
        cur = tx.execute(
            "INSERT INTO inbox (event_id, type, received_at) VALUES (?, ?, ?) ON CONFLICT(event_id) DO NOTHING",
            (ev["id"], ev["type"], int(time.time() * 1000)),
        )
        if cur.rowcount == 0:
            return False
        handle(tx, ev)
        return True


# ---- outbox ----

def enqueue(tx: sqlite3.Connection, target: str, event_type: str, payload: Any) -> str:
    """Record an event inside the caller's transaction; it exists only if that commits."""
    event_id, now = new_id(), int(time.time() * 1000)
    tx.execute(
        "INSERT INTO outbox (id, target, type, payload, created_at, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?)",
        (event_id, target, event_type, json.dumps(payload, separators=(",", ":")), now, now),
    )
    return event_id


def pending_count(db: Database) -> int:
    row = db.one("SELECT COUNT(*) AS n FROM outbox WHERE delivered_at IS NULL")
    return int(row["n"])


def backoff(attempts: int, cap: float) -> float:
    """1s, 2s, 4s, ... capped."""
    if attempts > 10:
        return cap
    return min(float(1 << (attempts - 1)), cap)


class Dispatcher:
    """Background thread that POSTs pending outbox rows to their target service."""

    def __init__(self, db: Database, source: str, targets: dict[str, str], interval: float = 2.0,
                 max_backoff: float = 60.0, batch: int = 50, timeout: float = 5.0):
        self.db, self.source, self.targets = db, source, targets
        self.interval, self.max_backoff, self.batch, self.timeout = interval, max_backoff, batch, timeout
        self._wake = threading.Event()
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="outbox-dispatcher", daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        self._wake.set()
        if self._thread:
            self._thread.join(timeout=5)

    def notify(self) -> None:
        """Ask for an immediate pass (call after the enqueuing transaction commits)."""
        self._wake.set()

    def _run(self) -> None:
        last_purge = time.monotonic()
        while not self._stop.is_set():
            try:
                self.dispatch_once()
                if time.monotonic() - last_purge > 3600:
                    self.purge()
                    last_purge = time.monotonic()
            except Exception as err:  # keep the loop alive; the next pass retries
                log("error", "outbox dispatch failed", err=str(err))
            self._wake.wait(self.interval)
            self._wake.clear()

    def dispatch_once(self) -> int:
        """Run one delivery pass; returns how many events were delivered."""
        rows = self.db.read(
            "SELECT id, target, type, payload, created_at, attempts, next_attempt_at FROM outbox "
            "WHERE delivered_at IS NULL ORDER BY rowid LIMIT ?", (self.batch,))  # enqueue order; ms timestamps can tie
        now = int(time.time() * 1000)
        due, not_due = [], set()
        for r in rows:  # a not-yet-due row holds back later rows for its target, to keep order
            if r["target"] in not_due:
                continue
            if r["next_attempt_at"] > now:
                not_due.add(r["target"])
                continue
            due.append(r)

        delivered, blocked = 0, set()
        for r in due:
            if r["target"] in blocked:
                continue
            try:
                self._deliver(r)
            except Exception as err:
                blocked.add(r["target"])
                attempts = r["attempts"] + 1
                retry_at = int((time.time() + backoff(attempts, self.max_backoff)) * 1000)
                self.db.execute("UPDATE outbox SET attempts=?, next_attempt_at=?, last_error=? WHERE id=?",
                                (attempts, retry_at, str(err)[:300], r["id"]))
                log("warn", "outbox delivery failed", event_id=r["id"], type=r["type"], target=r["target"],
                    attempts=attempts, err=str(err)[:300])
                continue
            self.db.execute("UPDATE outbox SET delivered_at=?, last_error=NULL WHERE id=?", (int(time.time() * 1000), r["id"]))
            delivered += 1
        return delivered

    def _deliver(self, r: sqlite3.Row) -> None:
        url = self.targets.get(r["target"])
        if url is None:
            raise RuntimeError(f"unknown target {r['target']!r}")
        body = json.dumps({
            "id": r["id"], "type": r["type"], "source": self.source,
            "created_at": datetime.fromtimestamp(r["created_at"] / 1000, timezone.utc).isoformat().replace("+00:00", "Z"),
            "payload": json.loads(r["payload"]),
        }).encode()
        req = urllib.request.Request(url, data=body, method="POST",
                                     headers={"Content-Type": "application/json", "X-Event-ID": r["id"]})
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                resp.read()
        except urllib.error.HTTPError as err:
            raise RuntimeError(f"target answered {err.code}: {err.read(200).decode(errors='replace').strip()}") from err

    def purge(self, retention: float = 24 * 3600) -> None:
        self.db.execute("DELETE FROM outbox WHERE delivered_at IS NOT NULL AND delivered_at < ?",
                        (int((time.time() - retention) * 1000),))
