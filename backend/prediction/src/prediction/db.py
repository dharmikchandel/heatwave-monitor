"""SQLite access: WAL, one connection guarded by a lock, versioned migrations.

Mirrors backend/internal/sqlitex (Go): the core tables (outbox, inbox) come
from the same SQL file, and migrations are tracked per scope in schema_version.
"""

from __future__ import annotations

import os
import re
import sqlite3
import threading
import time
from contextlib import contextmanager
from importlib import resources
from typing import Iterator

_MIGRATION_NAME = re.compile(r"^(\d+)_.+\.sql$")


def _read(name: str) -> str:
    return resources.files("prediction").joinpath("migrations", name).read_text(encoding="utf-8")


class Database:
    """A single SQLite connection. Use ``transaction()`` for writes and ``read()`` for queries."""

    def __init__(self, path: str):
        if path != ":memory:":
            os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
        self._con = sqlite3.connect(path, check_same_thread=False, isolation_level=None)
        self._con.row_factory = sqlite3.Row
        self.lock = threading.RLock()
        for pragma in ("busy_timeout=5000", "journal_mode=WAL", "synchronous=NORMAL", "foreign_keys=ON"):
            self._con.execute(f"PRAGMA {pragma}")
        self._migrate_core()
        self._migrate("service", self._service_migrations())

    def close(self) -> None:
        with self.lock:
            self._con.close()

    @contextmanager
    def transaction(self) -> Iterator[sqlite3.Connection]:
        """Run a block atomically; an exception rolls everything back."""
        with self.lock:
            self._con.execute("BEGIN IMMEDIATE")
            try:
                yield self._con
            except BaseException:
                self._con.execute("ROLLBACK")
                raise
            else:
                self._con.execute("COMMIT")

    def read(self, sql: str, params: tuple = ()) -> list[sqlite3.Row]:
        with self.lock:
            return self._con.execute(sql, params).fetchall()

    def one(self, sql: str, params: tuple = ()) -> sqlite3.Row | None:
        rows = self.read(sql, params)
        return rows[0] if rows else None

    def execute(self, sql: str, params: tuple = ()) -> int:
        with self.transaction() as tx:
            return tx.execute(sql, params).rowcount

    # ---- migrations ----

    def _migrate_core(self) -> None:
        self._migrate("core", [(1, "0001_outbox_inbox.sql", _read("core_0001_outbox_inbox.sql.txt"))])

    @staticmethod
    def _service_migrations() -> list[tuple[int, str, str]]:
        found = []
        for entry in resources.files("prediction").joinpath("migrations").iterdir():
            m = _MIGRATION_NAME.match(entry.name)
            if m:
                found.append((int(m.group(1)), entry.name, entry.read_text(encoding="utf-8")))
        found.sort()
        for (a, an, _), (b, bn, _) in zip(found, found[1:]):
            if a == b:
                raise ValueError(f"migrations {an} and {bn} share version {a}")
        return found

    def _migrate(self, scope: str, migrations: list[tuple[int, str, str]]) -> None:
        with self.lock:
            self._con.execute(
                "CREATE TABLE IF NOT EXISTS schema_version ("
                "scope TEXT NOT NULL, version INTEGER NOT NULL, name TEXT NOT NULL, applied_at INTEGER NOT NULL, "
                "PRIMARY KEY (scope, version))"
            )
            for version, name, sql in migrations:
                if self._con.execute("SELECT 1 FROM schema_version WHERE scope=? AND version=?", (scope, version)).fetchone():
                    continue
                try:
                    self._con.executescript(f"BEGIN;\n{sql}\n")
                    self._con.execute(
                        "INSERT INTO schema_version (scope, version, name, applied_at) VALUES (?, ?, ?, ?)",
                        (scope, version, name, int(time.time() * 1000)),
                    )
                    self._con.execute("COMMIT")
                except BaseException:
                    if self._con.in_transaction:
                        self._con.execute("ROLLBACK")
                    raise
