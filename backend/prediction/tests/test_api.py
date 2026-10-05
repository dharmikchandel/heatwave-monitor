import copy
import json

from fastapi.testclient import TestClient

from prediction.app import create_app

HOT_DAYS = [
    {"date": f"2026-05-0{i + 1}", "tempMaxC": 44.0, "tempMinC": 31.0, "apparentTempMaxC": 48.0} for i in range(5)
]


def err_code(resp):
    return resp.json()["error"]["code"]


def test_operational_endpoints(client):
    assert client.get("/healthz").json() == {"status": "ok", "service": "prediction"}
    ready = client.get("/readyz")
    assert ready.status_code == 200 and ready.json()["checks"] == {"database": "ok", "model": "ok"}

    text = client.get("/metrics").text
    for want in ('service_up{service="prediction"} 1', "prediction_events_total", "outbox_pending_events 0",
                 'http_requests_total{service="prediction",method="GET",route="GET /healthz",code="200"}'):
        assert want in text, want


def test_event_endpoint_then_prediction_endpoint(client, processed_sample, make_event):
    assert client.get("/locations/1/prediction").status_code == 404

    ev = make_event(processed_sample, event_id="e1")
    first = client.post("/internal/events", json=ev)
    assert first.status_code == 202 and first.json() == {"event_id": "e1", "duplicate": False}
    again = client.post("/internal/events", json=ev)
    assert again.status_code == 202 and again.json()["duplicate"] is True

    got = client.get("/locations/1/prediction")
    assert got.status_code == 200
    body = got.json()
    assert body["locationId"] == 1 and body["prediction"]["method"] == "model"
    assert len(body["prediction"]["days"]) == 7

    assert "outbox_pending_events 1" in client.get("/metrics").text
    assert 'prediction_events_total{result="predicted"} 1' in client.get("/metrics").text


def test_malformed_envelopes_are_rejected_with_400(client):
    for body in ({}, {"type": "t", "payload": {}}, {"id": "x", "payload": {}}, {"id": "x", "type": "t"},
                 {"id": "x", "type": "t", "payload": "str"}):
        resp = client.post("/internal/events", json=body)
        assert resp.status_code == 400 and err_code(resp) in ("bad_request", "validation_failed"), body
    assert client.post("/internal/events", content=b"{not json", headers={"content-type": "application/json"}).status_code == 400


def test_invalid_event_payloads_are_accepted_and_visible_as_rejections(client, processed_sample, make_event):
    bad = copy.deepcopy(processed_sample)
    bad["days"] = []
    assert client.post("/internal/events", json=make_event(bad)).status_code == 202  # consumed, not retried

    listing = client.get("/rejections").json()["rejections"]
    assert len(listing) == 1 and listing[0]["reason"] == "invalid_payload" and listing[0]["locationId"] == 1
    for limit in ("0", "501", "abc", "-1"):
        resp = client.get(f"/rejections?limit={limit}")
        assert resp.status_code == 400 and err_code(resp) == "invalid_limit"
    assert len(client.get("/rejections?limit=1").json()["rejections"]) == 1


def test_prediction_endpoint_validates_the_id(client):
    for bad in ("abc", "0", "-3", "1.5", "%CE%A9"):
        resp = client.get(f"/locations/{bad}/prediction")
        assert resp.status_code == 400 and err_code(resp) == "invalid_id", bad


def test_predict_endpoint(client):
    resp = client.post("/predict", json={"days": HOT_DAYS, "firstForecastIndex": 1})
    assert resp.status_code == 200
    body = resp.json()
    assert body["method"] == "model" and [d["horizon"] for d in body["days"]] == [0, 1, 2, 3]
    assert body["days"][0]["date"] == "2026-05-02"
    assert body["days"][-1]["probability"] > 0.9  # a long hot run

    # Days may arrive in any order: they are sorted by date before the model sees them.
    shuffled = client.post("/predict", json={"days": list(reversed(HOT_DAYS)), "firstForecastIndex": 1}).json()
    assert [d["probability"] for d in shuffled["days"]] == [d["probability"] for d in body["days"]]


def test_predict_endpoint_validation(client):
    cases = {
        "no days": {"days": []},
        "index past the end": {"days": HOT_DAYS, "firstForecastIndex": 5},
        "negative index": {"days": HOT_DAYS, "firstForecastIndex": -1},
        "unknown field": {"days": HOT_DAYS, "latitude": 1},
        "bad date": {"days": [{**HOT_DAYS[0], "date": "tomorrow"}]},
        "wrong type": {"days": [{**HOT_DAYS[0], "tempMaxC": "hot"}]},
        "missing field": {"days": [{k: v for k, v in HOT_DAYS[0].items() if k != "tempMinC"}]},
    }
    for name, body in cases.items():
        resp = client.post("/predict", json=body)
        assert resp.status_code == 400 and err_code(resp) == "validation_failed", (name, resp.status_code, resp.text)
        assert resp.json()["request_id"]


def test_model_endpoint(client, model):
    info = client.get("/model").json()
    assert info["loaded"] is True and info["version"] == model.version and info["horizons"] == 7
    assert info["evaluation"]["perHorizon"] and info["training"]["cities"]


def test_error_shape_and_request_ids(client):
    resp = client.get("/nope")
    assert resp.status_code == 404 and err_code(resp) == "not_found" and resp.json()["request_id"]
    assert client.delete("/healthz").status_code == 405

    assert client.get("/healthz", headers={"X-Request-ID": "trace-1"}).headers["x-request-id"] == "trace-1"
    generated = client.get("/healthz", headers={"X-Request-ID": "bad id\nwith newline"}).headers["x-request-id"]
    assert generated and " " not in generated and "\n" not in generated
    assert resp.headers["x-request-id"] == resp.json()["request_id"]


def test_without_a_model_the_service_stays_up_on_rules(tmp_path, processed_sample, make_event):
    app = create_app(str(tmp_path / "p.db"), model_path=tmp_path / "missing.json", start_dispatcher=False)
    with TestClient(app) as c:
        ready = c.get("/readyz")
        assert ready.status_code == 200 and "unavailable" in ready.json()["checks"]["model"]
        assert c.get("/model").json()["loaded"] is False
        assert c.post("/predict", json={"days": HOT_DAYS}).json()["method"] == "rules"
        c.post("/internal/events", json=make_event(processed_sample))
        assert c.get("/locations/1/prediction").json()["prediction"]["method"] == "rules"


def test_a_corrupt_model_file_degrades_instead_of_crashing(tmp_path):
    bad = tmp_path / "bad.json"
    bad.write_text(json.dumps({"version": "x", "features": ["nope"], "horizons": []}))
    app = create_app(str(tmp_path / "p.db"), model_path=bad, start_dispatcher=False)
    with TestClient(app) as c:
        assert c.get("/model").json()["loaded"] is False


def test_predicted_event_reaches_the_risk_service_promptly(tmp_path, processed_sample, make_event):
    """The dispatcher polls every 2s, so delivery inside 1s proves it is woken after the commit."""
    import time

    from test_events import Receiver

    risk = Receiver()
    try:
        app = create_app(str(tmp_path / "p.db"), risk_url=risk.url, start_dispatcher=True)
        with TestClient(app) as c:
            time.sleep(0.2)  # let the dispatcher finish its first (empty) pass and go to sleep
            assert c.post("/internal/events", json=make_event(processed_sample)).status_code == 202
            deadline = time.time() + 1.0
            while not risk.got and time.time() < deadline:
                time.sleep(0.02)
        assert len(risk.got) == 1, "the event was not delivered within 1s; the dispatcher was not woken after commit"
        ev = risk.got[0]
        assert ev["type"] == "heatwave.predicted" and ev["source"] == "prediction"
        assert ev["payload"]["prediction"]["method"] == "model" and ev["payload"]["weather"]["location"]["id"] == 1
    finally:
        risk.close()
