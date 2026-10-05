import copy
from datetime import datetime, timedelta, timezone

import pytest

from prediction.db import Database
from prediction.events import pending_count, receive
from prediction.model import Model
from prediction.service import PredictionService, parse_timestamp


def send(db, service, event):
    return receive(db, event, service.handle_event)


def outbox_rows(db):
    return db.read("SELECT target, type, payload FROM outbox")


def at(payload, minutes):
    p = copy.deepcopy(payload)
    p["fetchedAt"] = (datetime(2026, 5, 1, 9, tzinfo=timezone.utc) + timedelta(minutes=minutes)).strftime("%Y-%m-%dT%H:%M:%SZ")
    return p


def test_predicts_stores_and_queues_for_risk(db, service, processed_sample, make_event):
    assert send(db, service, make_event(processed_sample)) is True

    stored = service.latest(1)
    assert stored["observationId"] == 42 and stored["locationId"] == 1
    pred = stored["prediction"]
    assert pred["method"] == "model" and pred["modelVersion"] == service.model.version
    forecast_days = [d for d in processed_sample["days"] if d["forecast"]]
    assert [d["date"] for d in pred["days"]] == [d["date"] for d in forecast_days]
    assert [d["horizon"] for d in pred["days"]] == list(range(len(forecast_days)))
    assert pred["peak"] == max(pred["days"], key=lambda d: (d["probability"], -d["horizon"]))

    (row,) = outbox_rows(db)
    assert (row["target"], row["type"]) == ("risk", "heatwave.predicted")
    import json

    out = json.loads(row["payload"])
    assert out["weather"] == processed_sample  # passed through untouched
    assert out["prediction"]["peak"] == pred["peak"]
    assert service.counts == {"predicted": 1, "rejected": 0, "stale": 0}
    assert service.methods["model"] == 1


def test_the_building_heatwave_scenario_is_flagged_likely(db, service, processed_sample, make_event):
    send(db, service, make_event(processed_sample))
    # The sample is the Go simulator's "building" scenario: apparent temperatures climb from 41.8 C
    # to 55 C over the week. Today is the first day over 41, so it is not yet a warning (a warning
    # needs two hot days in a row); every day after it is.
    probs = [d["probability"] for d in service.latest(1)["prediction"]["days"]]
    assert probs[0] < probs[1]
    assert all(p > 0.8 for p in probs[1:5]), probs
    assert service.latest(1)["prediction"]["peak"]["probability"] > 0.9


def test_redelivery_is_processed_once(db, service, processed_sample, make_event):
    ev = make_event(processed_sample, event_id="same")
    assert [send(db, service, ev) for _ in range(3)] == [True, False, False]
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 1 and pending_count(db) == 1


@pytest.mark.parametrize("mutate,reason,detail", [
    (lambda p: p.pop("days"), "invalid_payload", "days"),
    (lambda p: p.update(days=[]), "invalid_payload", "days"),
    (lambda p: p["location"].update(id=0), "invalid_payload", "location.id"),
    (lambda p: p["location"].update(latitude=123), "invalid_payload", "location.latitude"),
    (lambda p: p.update(fetchedAt="yesterday-ish"), "invalid_payload", "fetchedAt"),
    (lambda p: p["days"][0].update(tempMaxC="hot"), "invalid_payload", "days.0.tempMaxC"),
    (lambda p: [d.update(forecast=False) for d in p["days"]], "no_forecast_days", "forecast"),
])
def test_bad_payloads_are_recorded_and_consumed(db, service, processed_sample, make_event, mutate, reason, detail):
    payload = copy.deepcopy(processed_sample)
    mutate(payload)
    assert send(db, service, make_event(payload, event_id="bad")) is True  # consumed: the producer must not retry

    (rej,) = service.recent_rejections(10)
    assert rej["reason"] == reason and detail in rej["detail"], rej
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 0 and pending_count(db) == 0
    assert service.counts["rejected"] == 1


def test_rejection_keeps_identifiers_when_it_can(db, service, processed_sample, make_event):
    payload = copy.deepcopy(processed_sample)
    payload["days"] = []
    send(db, service, make_event(payload))
    (rej,) = service.recent_rejections(10)
    assert rej["locationId"] == 1 and rej["observationId"] == 42

    send(db, service, make_event({"junk": True}))
    assert service.recent_rejections(10)[0]["locationId"] is None


def test_out_of_order_delivery_never_replaces_newer_data(db, service, processed_sample, make_event):
    send(db, service, make_event(at(processed_sample, 10)))
    send(db, service, make_event(at(processed_sample, 0)))   # late
    send(db, service, make_event(at(processed_sample, 10)))  # same instant
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 1 and pending_count(db) == 1
    assert service.counts["stale"] == 2

    other = at(processed_sample, 0)
    other["location"]["id"] = 2  # a different location has its own clock
    send(db, service, make_event(other))
    send(db, service, make_event(at(processed_sample, 20)))
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 3


def test_unknown_event_type_is_ignored(db, service, make_event):
    assert send(db, service, make_event({}, event_type="something.else")) is True
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 0 and not service.recent_rejections(5)


def test_transient_failure_rolls_back_so_the_retry_works(db, service, processed_sample, make_event):
    ev = make_event(processed_sample, event_id="retry")
    db.execute("ALTER TABLE outbox RENAME TO outbox_away")
    with pytest.raises(Exception):
        send(db, service, ev)
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 0
    assert db.one("SELECT COUNT(*) AS n FROM inbox")["n"] == 0  # no dedupe record, so the retry will not be skipped

    db.execute("ALTER TABLE outbox_away RENAME TO outbox")
    assert send(db, service, ev) is True
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 1 and pending_count(db) == 1


def test_prediction_does_not_depend_on_where_the_location_is(db, model, processed_sample, make_event):
    service = PredictionService(db, model)
    here = copy.deepcopy(processed_sample)
    there = copy.deepcopy(processed_sample)
    there["location"].update(id=2, latitude=-33.9, longitude=151.2, name="Sydney")
    send(db, service, make_event(here))
    send(db, service, make_event(there))
    a, b = service.latest(1)["prediction"], service.latest(2)["prediction"]
    assert a["method"] == b["method"] == "model"
    assert [d["probability"] for d in a["days"]] == [d["probability"] for d in b["days"]]


def test_missing_model_falls_back_to_rules(db, processed_sample, make_event):
    service = PredictionService(db, None)
    send(db, service, make_event(processed_sample))
    assert service.latest(1)["prediction"]["method"] == "rules"


def test_history_days_are_context_not_predictions(db, service, processed_sample, make_event):
    send(db, service, make_event(processed_sample))
    predicted = {d["date"] for d in service.latest(1)["prediction"]["days"]}
    history = {d["date"] for d in processed_sample["days"] if not d["forecast"]}
    assert history and not (predicted & history)


def test_purge_removes_only_expired_rows(db, model):
    svc = PredictionService(db, model, retention=24 * 3600, rejection_retention=7 * 24 * 3600)
    now = int(datetime.now(timezone.utc).timestamp() * 1000)
    day = 24 * 3600 * 1000
    for age in (2 * day, 3600 * 1000):
        db.execute("INSERT INTO predictions (location_id, observation_id, fetched_at, generated_at, method, model_version, "
                   "peak_probability, payload) VALUES (1, 1, ?, ?, 'model', 'v', 0.1, '{}')", (now - age, now))
    for age in (10 * day, day):
        db.execute("INSERT INTO rejections (event_id, reason, detail, created_at) VALUES ('e', 'r', 'd', ?)", (now - age,))
    svc.purge()
    assert db.one("SELECT COUNT(*) AS n FROM predictions")["n"] == 1
    assert db.one("SELECT COUNT(*) AS n FROM rejections")["n"] == 1


def test_parses_go_timestamps_with_nanoseconds():
    ts = parse_timestamp("2026-10-04T18:37:47.142357339Z")
    assert ts == datetime(2026, 10, 4, 18, 37, 47, 142357, tzinfo=timezone.utc)
    assert parse_timestamp("2026-05-01T09:00:00Z") == datetime(2026, 5, 1, 9, tzinfo=timezone.utc)
    assert parse_timestamp("2026-05-01T14:30:00+05:30").utcoffset() == timedelta(hours=5, minutes=30)
    assert parse_timestamp("2026-05-01T09:00:00").tzinfo is not None
    with pytest.raises(ValueError):
        parse_timestamp("not a time")
