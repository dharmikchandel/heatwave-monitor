"""Turns weather.processed events into heatwave probabilities."""

from __future__ import annotations

import json
import re
import sqlite3
import threading
import time
from datetime import date, datetime, timezone
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, ValidationError

from .db import Database
from .events import enqueue
from .features import Day
from .logs import log
from .model import METHOD_MODEL, Model, predict_days

EVENT_WEATHER_PROCESSED = "weather.processed"
EVENT_HEATWAVE_PREDICTED = "heatwave.predicted"
TARGET_RISK = "risk"

RULES_VERSION = "rules-v1"

REASON_INVALID = "invalid_payload"
REASON_NO_FORECAST = "no_forecast_days"


class DayIn(BaseModel):
    model_config = ConfigDict(extra="allow")
    date: date
    forecast: bool
    tempMaxC: float
    tempMinC: float
    apparentTempMaxC: float


class LocationIn(BaseModel):
    model_config = ConfigDict(extra="allow")
    id: int = Field(gt=0)
    latitude: float = Field(ge=-90, le=90)
    longitude: float = Field(ge=-180, le=180)


class ProcessedIn(BaseModel):
    """The parts of a weather.processed payload this service needs. Everything else
    is passed through untouched to the next service."""

    model_config = ConfigDict(extra="allow")
    observationId: int
    location: LocationIn
    fetchedAt: str
    days: list[DayIn] = Field(min_length=1)


class RejectError(Exception):
    def __init__(self, reason: str, detail: str):
        super().__init__(f"{reason}: {detail}")
        self.reason, self.detail = reason, detail


_FRACTION = re.compile(r"(\.\d{6})\d+")


def parse_timestamp(value: str) -> datetime:
    """Parse RFC 3339 timestamps as Go writes them (nanosecond precision, 'Z' suffix)."""
    dt = datetime.fromisoformat(_FRACTION.sub(r"\1", value).replace("Z", "+00:00"))
    return dt if dt.tzinfo else dt.replace(tzinfo=timezone.utc)


def to_series(days: list[DayIn]) -> tuple[list[Day], int]:
    """Convert payload days to the model's series, oldest first, plus the index of the first forecast day."""
    ordered = sorted(days, key=lambda d: d.date)
    series = [Day(d.date, d.tempMaxC, d.tempMinC, d.apparentTempMaxC) for d in ordered]
    first = next((i for i, d in enumerate(ordered) if d.forecast), None)
    if first is None:
        raise RejectError(REASON_NO_FORECAST, "payload contains no forecast days")
    return series, first


def build_prediction(series: list[Day], first_forecast: int, model: Model | None) -> dict[str, Any]:
    method, days = predict_days(series, first_forecast, model)
    peak = max(days, key=lambda d: (d.probability, -d.horizon))  # highest probability, earliest day on ties
    return {
        "modelVersion": model.version if method == METHOD_MODEL and model else RULES_VERSION,
        "method": method,
        "generatedAt": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z",
        "days": [{"date": d.date, "horizon": d.horizon, "probability": d.probability} for d in days],
        "peak": {"date": peak.date, "horizon": peak.horizon, "probability": peak.probability},
    }


class PredictionService:
    def __init__(self, db: Database, model: Model | None, retention: float = 24 * 3600, rejection_retention: float = 7 * 24 * 3600):
        self.db, self.model = db, model
        self.retention, self.rejection_retention = retention, rejection_retention
        self._lock = threading.Lock()
        self.counts = {"predicted": 0, "rejected": 0, "stale": 0}
        self.methods = {"model": 0, "rules": 0}

    def _count(self, key: str) -> None:
        with self._lock:
            self.counts[key] += 1

    # ---- event handling (runs inside the inbox transaction) ----

    def handle_event(self, tx: sqlite3.Connection, ev: dict) -> None:
        if ev["type"] != EVENT_WEATHER_PROCESSED:
            log("warn", "ignoring unknown event type", type=ev["type"], event_id=ev["id"])
            return
        payload = ev["payload"]
        try:
            self._handle(tx, ev, payload)
        except RejectError as rej:
            self._reject(tx, ev, payload, rej)

    def _handle(self, tx: sqlite3.Connection, ev: dict, payload: Any) -> None:
        try:
            parsed = ProcessedIn.model_validate(payload)
            fetched_at = parse_timestamp(parsed.fetchedAt)
        except ValidationError as err:  # (a ValueError subclass, so it must be caught first)
            raise RejectError(REASON_INVALID, _first_error(err)) from err
        except ValueError as err:  # e.g. an unparseable fetchedAt timestamp
            raise RejectError(REASON_INVALID, f"fetchedAt: {err}") from err

        series, first = to_series(parsed.days)
        loc = parsed.location

        row = tx.execute("SELECT MAX(fetched_at) FROM predictions WHERE location_id = ?", (loc.id,)).fetchone()
        fetched_ms = int(fetched_at.timestamp() * 1000)
        if row[0] is not None and fetched_ms <= row[0]:
            self._count("stale")
            log("info", "dropping stale observation", location_id=loc.id, observation_id=parsed.observationId)
            return

        prediction = build_prediction(series, first, self.model)
        record = {"locationId": loc.id, "observationId": parsed.observationId, "fetchedAt": parsed.fetchedAt, "prediction": prediction}
        tx.execute(
            "INSERT INTO predictions (location_id, observation_id, fetched_at, generated_at, method, model_version, peak_probability, payload) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
            (loc.id, parsed.observationId, fetched_ms, int(time.time() * 1000), prediction["method"],
             prediction["modelVersion"], prediction["peak"]["probability"], json.dumps(record)),
        )
        enqueue(tx, TARGET_RISK, EVENT_HEATWAVE_PREDICTED, {"weather": payload, "prediction": prediction})

        self._count("predicted")
        with self._lock:
            self.methods[prediction["method"]] += 1
        log("info", "predicted heatwave probability", location_id=loc.id, observation_id=parsed.observationId,
            method=prediction["method"], peak_probability=prediction["peak"]["probability"], peak_date=prediction["peak"]["date"])

    def _reject(self, tx: sqlite3.Connection, ev: dict, payload: Any, rej: RejectError) -> None:
        loc_id = obs_id = None
        if isinstance(payload, dict):
            loc = payload.get("location")
            loc_id = loc.get("id") if isinstance(loc, dict) and isinstance(loc.get("id"), int) else None
            obs_id = payload.get("observationId") if isinstance(payload.get("observationId"), int) else None
        tx.execute(
            "INSERT INTO rejections (event_id, location_id, observation_id, reason, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)",
            (ev["id"], loc_id, obs_id, rej.reason, rej.detail, int(time.time() * 1000)),
        )
        self._count("rejected")
        log("warn", "rejected observation", event_id=ev["id"], location_id=loc_id, reason=rej.reason, detail=rej.detail)

    # ---- queries ----

    def latest(self, location_id: int) -> dict | None:
        row = self.db.one("SELECT payload FROM predictions WHERE location_id = ? ORDER BY fetched_at DESC, id DESC LIMIT 1", (location_id,))
        return json.loads(row["payload"]) if row else None

    def recent_rejections(self, limit: int) -> list[dict]:
        rows = self.db.read(
            "SELECT event_id, location_id, observation_id, reason, detail, created_at FROM rejections "
            "ORDER BY created_at DESC, id DESC LIMIT ?", (limit,))
        return [{
            "eventId": r["event_id"], "locationId": r["location_id"], "observationId": r["observation_id"],
            "reason": r["reason"], "detail": r["detail"],
            "at": datetime.fromtimestamp(r["created_at"] / 1000, timezone.utc).isoformat().replace("+00:00", "Z"),
        } for r in rows]

    def purge(self) -> None:
        now = time.time()
        self.db.execute("DELETE FROM predictions WHERE fetched_at < ?", (int((now - self.retention) * 1000),))
        self.db.execute("DELETE FROM rejections WHERE created_at < ?", (int((now - self.rejection_retention) * 1000),))


def _first_error(err: ValidationError) -> str:
    e = err.errors()[0]
    where = ".".join(str(p) for p in e["loc"]) or "payload"
    return f"{where}: {e['msg']}"
