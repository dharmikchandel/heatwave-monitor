import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from prediction.db import Database
from prediction.events import Dispatcher, backoff, enqueue, new_id, pending_count, receive, validate_envelope


class Receiver:
    """A fake downstream service that records events and can be told to fail."""

    def __init__(self):
        self.got, self.status = [], 202
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                if outer.status // 100 == 2:
                    outer.got.append(body)
                self.send_response(outer.status)
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *a):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}/internal/events"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()


@pytest.fixture
def receiver():
    r = Receiver()
    yield r
    r.close()


def queue(db, target, payload, typ="heatwave.predicted"):
    with db.transaction() as tx:
        return enqueue(tx, target, typ, payload)


def test_delivers_the_wire_envelope(db, receiver):
    event_id = queue(db, "risk", {"n": 7})
    d = Dispatcher(db, "prediction", {"risk": receiver.url})
    assert d.dispatch_once() == 1
    (ev,) = receiver.got
    assert ev["id"] == event_id and ev["type"] == "heatwave.predicted" and ev["source"] == "prediction"
    assert ev["payload"] == {"n": 7}
    assert ev["created_at"].endswith("Z")  # RFC 3339, as the Go consumers expect
    assert pending_count(db) == 0
    assert d.dispatch_once() == 0 and len(receiver.got) == 1  # delivered rows are not re-sent


def test_rollback_leaves_no_event(db):
    with pytest.raises(RuntimeError):
        with db.transaction() as tx:
            enqueue(tx, "risk", "t", {"a": 1})
            raise RuntimeError("business logic failed")
    assert pending_count(db) == 0


def test_retries_with_backoff_then_succeeds(db, receiver):
    receiver.status = 500
    queue(db, "risk", {"n": 1})
    d = Dispatcher(db, "prediction", {"risk": receiver.url})
    assert d.dispatch_once() == 0
    row = db.one("SELECT attempts, last_error, next_attempt_at FROM outbox")
    assert row["attempts"] == 1 and "500" in row["last_error"]
    assert row["next_attempt_at"] > time.time() * 1000  # backed off

    receiver.status = 202
    assert d.dispatch_once() == 0  # still backing off
    db.execute("UPDATE outbox SET next_attempt_at = 0")
    assert d.dispatch_once() == 1 and len(receiver.got) == 1


def test_preserves_order_per_target_and_isolates_targets(db, receiver):
    bad, good = Receiver(), receiver
    bad.status = 503
    try:
        queue(db, "bad", "bad-1")
        queue(db, "bad", "bad-2")
        queue(db, "good", "good-1")
        d = Dispatcher(db, "s", {"bad": bad.url, "good": good.url})
        assert d.dispatch_once() == 1 and len(good.got) == 1
        assert db.one("SELECT attempts FROM outbox WHERE payload = ?", ('"bad-2"',))["attempts"] == 0

        bad.status = 202
        db.execute("UPDATE outbox SET next_attempt_at = 0")
        d.dispatch_once()
        assert [e["payload"] for e in bad.got] == ["bad-1", "bad-2"]
    finally:
        bad.close()


def test_unreachable_and_unknown_targets_keep_the_event(db):
    queue(db, "gone", 1)
    queue(db, "nowhere", 2)
    d = Dispatcher(db, "s", {"gone": "http://127.0.0.1:1/x"}, timeout=0.5)
    assert d.dispatch_once() == 0
    assert pending_count(db) == 2  # nothing is dropped because of an outage or a config mistake


def test_notify_wakes_the_background_thread(db, receiver):
    d = Dispatcher(db, "prediction", {"risk": receiver.url}, interval=3600)
    d.start()
    try:
        time.sleep(0.1)  # initial empty pass, then it sleeps for an hour
        queue(db, "risk", 1)
        d.notify()
        deadline = time.time() + 3
        while not receiver.got and time.time() < deadline:
            time.sleep(0.02)
        assert len(receiver.got) == 1
    finally:
        d.stop()


def test_backoff_grows_and_caps():
    assert [backoff(n, 30) for n in range(1, 8)] == [1, 2, 4, 8, 16, 30, 30]
    assert backoff(500, 30) == 30


def test_receive_deduplicates_and_rolls_back_on_failure(db):
    db.execute("CREATE TABLE applied (n INTEGER)")
    ev = {"id": "e1", "type": "t", "payload": {}}

    def apply(tx, _):
        tx.execute("INSERT INTO applied VALUES (1)")

    assert receive(db, ev, apply) is True
    assert receive(db, ev, apply) is False
    assert db.one("SELECT COUNT(*) AS n FROM applied")["n"] == 1

    def boom(tx, _):
        tx.execute("INSERT INTO applied VALUES (2)")
        raise RuntimeError("boom")

    with pytest.raises(RuntimeError):
        receive(db, {"id": "e2", "type": "t", "payload": {}}, boom)
    assert db.one("SELECT COUNT(*) AS n FROM applied")["n"] == 1
    assert receive(db, {"id": "e2", "type": "t", "payload": {}}, apply) is True  # the retry is not skipped


def test_envelope_validation():
    assert validate_envelope({"id": "x", "type": "t", "payload": {}}) is None
    for bad in (None, [], {"type": "t", "payload": {}}, {"id": "x", "payload": {}}, {"id": "x", "type": "t"},
                {"id": "", "type": "t", "payload": {}}, {"id": "x", "type": "t", "payload": "str"}):
        assert validate_envelope(bad)


def test_ids_are_unique_and_time_sortable():
    ids = [new_id() for _ in range(500)]
    assert len(set(ids)) == 500 and all(len(i) == 28 for i in ids)
    assert [i[:12] for i in ids] == sorted(i[:12] for i in ids)


def test_migrations_apply_once_and_core_matches_go(tmp_path):
    from pathlib import Path

    path = str(tmp_path / "x.db")
    for _ in range(2):  # reopening must not re-run migrations
        d = Database(path)
        n = d.one("SELECT COUNT(*) AS n FROM schema_version")["n"]
        d.close()
        assert n == 2  # core 0001 + service 0001
    go = Path(__file__).resolve().parents[2] / "internal" / "sqlitex" / "core" / "0001_outbox_inbox.sql"
    py = Path(__file__).resolve().parents[1] / "src" / "prediction" / "migrations" / "core_0001_outbox_inbox.sql.txt"
    assert py.read_text() == go.read_text(), "the Python copy of the core outbox/inbox schema drifted from the Go one"


def test_same_millisecond_events_keep_enqueue_order(db, receiver):
    """IDs are time-prefixed but random within a millisecond, so they cannot order a burst."""
    with db.transaction() as tx:
        for i in range(50):
            enqueue(tx, "risk", "e", i)
    d = Dispatcher(db, "prediction", {"risk": receiver.url})
    assert d.dispatch_once() == 50
    assert [e["payload"] for e in receiver.got] == list(range(50))
