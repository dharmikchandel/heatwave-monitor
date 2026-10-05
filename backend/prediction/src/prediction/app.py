"""HTTP API for the prediction service (FastAPI)."""

from __future__ import annotations

import asyncio
import json
import re
import threading
import time
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from datetime import date
from pathlib import Path

from fastapi import Body, FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, PlainTextResponse
from pydantic import BaseModel, ConfigDict, Field
from starlette.exceptions import HTTPException as StarletteHTTPException

from . import logs
from .config import env_duration, env_str
from .db import Database
from .events import Dispatcher, new_id, pending_count, receive, validate_envelope
from .features import Day
from .logs import log
from .model import DEFAULT_MODEL_PATH, Model
from .service import PredictionService, build_prediction

SERVICE = "prediction"
_REQUEST_ID = re.compile(r"^[A-Za-z0-9_-]{1,64}$")


def error_response(status: int, code: str, message: str, request: Request | None = None) -> JSONResponse:
    body: dict = {"error": {"code": code, "message": message}}
    request_id = getattr(request.state, "request_id", None) if request else None
    if request_id:
        body["request_id"] = request_id
    return JSONResponse(body, status_code=status)


@dataclass
class Metrics:
    """Minimal Prometheus-format counters, matching the Go services' metric names."""

    started: float = field(default_factory=time.time)
    requests: dict = field(default_factory=dict)
    duration: dict = field(default_factory=dict)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def observe(self, method: str, route: str, code: int, seconds: float) -> None:
        with self.lock:
            self.requests[(method, route, code)] = self.requests.get((method, route, code), 0) + 1
            self.duration[(method, route)] = self.duration.get((method, route), 0.0) + seconds


class DayInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    date: date
    tempMaxC: float
    tempMinC: float
    apparentTempMaxC: float


class PredictRequest(BaseModel):
    """Stateless prediction: send a daily series, get probabilities back."""

    model_config = ConfigDict(extra="forbid")
    days: list[DayInput] = Field(min_length=1, max_length=31)
    firstForecastIndex: int = Field(default=0, ge=0)


def create_app(db_path: str | None = None, model_path: str | Path | None = None, risk_url: str | None = None,
               start_dispatcher: bool = True) -> FastAPI:
    db_path = db_path or env_str("DB_PATH", "data/prediction.db")
    risk_url = risk_url or env_str("RISK_URL", "http://localhost:8084/internal/events")
    model_file = Path(model_path or env_str("MODEL_PATH", str(DEFAULT_MODEL_PATH)))

    db = Database(db_path)
    model: Model | None = None
    try:
        model = Model.load(model_file)
        log("info", "model loaded", version=model.version, path=str(model_file))
    except (OSError, ValueError, KeyError) as err:
        log("error", "model unavailable; using the rule-based fallback for every location", path=str(model_file), err=str(err))

    svc = PredictionService(db, model, retention=env_duration("PREDICTION_RETENTION", 24 * 3600))
    dispatcher = Dispatcher(db, SERVICE, {"risk": risk_url})
    metrics = Metrics()

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        if start_dispatcher:
            dispatcher.start()
        purge_task = asyncio.create_task(_purge_hourly(svc))
        try:
            yield
        finally:
            purge_task.cancel()
            dispatcher.stop()
            db.close()

    app = FastAPI(title="Heatwave prediction service", lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)
    app.state.service, app.state.db, app.state.dispatcher, app.state.model = svc, db, dispatcher, model

    # ---- middleware: request IDs, access log, metrics, last-resort 500 ----

    @app.middleware("http")
    async def request_context(request: Request, call_next):
        start = time.perf_counter()
        inbound = request.headers.get("x-request-id", "")
        request.state.request_id = inbound if _REQUEST_ID.match(inbound) else new_id()[12:]
        try:
            response = await call_next(request)
        except Exception as err:
            log("error", "unhandled error", request_id=request.state.request_id, path=request.url.path, err=repr(err))
            response = error_response(500, "internal_error", "internal server error", request)
        response.headers["X-Request-ID"] = request.state.request_id

        route = request.scope.get("route")
        pattern = f"{request.method} {route.path}" if route else "unmatched"
        elapsed = time.perf_counter() - start
        metrics.observe(request.method, pattern, response.status_code, elapsed)
        quiet = pattern in ("GET /healthz", "GET /readyz", "GET /metrics")
        log("debug" if quiet else "info", "request", request_id=request.state.request_id, method=request.method,
            path=request.url.path, status=response.status_code, duration_ms=round(elapsed * 1000, 3))
        return response

    @app.exception_handler(RequestValidationError)
    async def validation_error(request: Request, exc: RequestValidationError):
        first = exc.errors()[0]
        where = ".".join(str(p) for p in first["loc"] if p != "body") or "body"
        return error_response(400, "validation_failed", f"{where}: {first['msg']}", request)

    @app.exception_handler(StarletteHTTPException)
    async def http_error(request: Request, exc: StarletteHTTPException):
        codes = {404: "not_found", 405: "method_not_allowed"}
        return error_response(exc.status_code, codes.get(exc.status_code, "http_error"), str(exc.detail), request)

    # ---- operational endpoints ----

    @app.get("/healthz")
    def healthz():
        return {"status": "ok", "service": SERVICE}

    @app.get("/readyz")
    def readyz():
        checks: dict[str, str] = {}
        try:
            db.one("SELECT 1")
            checks["database"] = "ok"
        except Exception as err:
            checks["database"] = str(err)
        checks["model"] = "ok" if model else "unavailable (rule-based fallback active)"
        ready = checks["database"] == "ok"  # a missing model degrades predictions but does not make the service unready
        return JSONResponse({"status": "ready" if ready else "unavailable", "service": SERVICE, "checks": checks},
                            status_code=200 if ready else 503)

    @app.get("/metrics")
    def metrics_endpoint():
        out = [f'service_up{{service="{SERVICE}"}} 1',
               f'process_start_time_seconds{{service="{SERVICE}"}} {int(metrics.started)}',
               "# TYPE http_requests_total counter"]
        with metrics.lock:
            for (method, route, code), n in sorted(metrics.requests.items(), key=lambda kv: (kv[0][1], kv[0][0], kv[0][2])):
                out.append(f'http_requests_total{{service="{SERVICE}",method="{method}",route="{route}",code="{code}"}} {n}')
            out.append("# TYPE http_request_duration_seconds_sum counter")
            for (method, route), secs in sorted(metrics.duration.items(), key=lambda kv: (kv[0][1], kv[0][0])):
                out.append(f'http_request_duration_seconds_sum{{service="{SERVICE}",method="{method}",route="{route}"}} {secs:g}')
        out.append("# TYPE prediction_events_total counter")
        for result, key in (("predicted", "predicted"), ("rejected", "rejected"), ("stale", "stale")):
            out.append(f'prediction_events_total{{result="{result}"}} {svc.counts[key]}')
        out.append("# TYPE prediction_method_total counter")
        for method, n in svc.methods.items():
            out.append(f'prediction_method_total{{method="{method}"}} {n}')
        out.append("# TYPE outbox_pending_events gauge")
        out.append(f"outbox_pending_events {pending_count(db)}")
        return PlainTextResponse("\n".join(out) + "\n", media_type="text/plain; version=0.0.4")

    # ---- business endpoints ----

    @app.post("/internal/events", status_code=202)
    def receive_event(request: Request, envelope: dict = Body(...)):
        problem = validate_envelope(envelope)
        if problem:
            return error_response(400, "bad_request", problem, request)
        try:
            processed = receive(db, envelope, svc.handle_event)
        except Exception as err:
            log("error", "event handling failed", request_id=request.state.request_id, event_id=envelope["id"], err=repr(err))
            return error_response(500, "event_failed", "event could not be processed", request)
        if processed:
            dispatcher.notify()  # the transaction has committed; the queued event is now visible
        return JSONResponse({"event_id": envelope["id"], "duplicate": not processed}, status_code=202)

    @app.get("/locations/{location_id}/prediction")
    def get_prediction(location_id: str, request: Request):
        if not location_id.isascii() or not location_id.isdigit() or int(location_id) <= 0:
            return error_response(400, "invalid_id", "location_id must be a positive integer", request)
        found = svc.latest(int(location_id))
        if found is None:
            return error_response(404, "no_data", "no prediction for this location yet", request)
        return found

    @app.post("/predict")
    def predict(body: PredictRequest, request: Request):
        if body.firstForecastIndex >= len(body.days):
            return error_response(400, "validation_failed", "firstForecastIndex must point at one of the days", request)
        days = sorted(body.days, key=lambda d: d.date)
        series = [Day(d.date, d.tempMaxC, d.tempMinC, d.apparentTempMaxC) for d in days]
        return build_prediction(series, body.firstForecastIndex, model)

    @app.get("/model")
    def model_info():
        if model is None:
            return {"loaded": False, "method": "rules", "rulesVersion": "rules-v1"}
        return {"loaded": True, **model.info()}

    @app.get("/rejections")
    def rejections(request: Request, limit: str = "50"):
        if not limit.isdigit() or not 1 <= int(limit) <= 500:
            return error_response(400, "invalid_limit", "limit must be between 1 and 500", request)
        return {"rejections": svc.recent_rejections(int(limit))}

    return app


async def _purge_hourly(svc: PredictionService) -> None:
    while True:
        await asyncio.sleep(3600)
        try:
            await asyncio.to_thread(svc.purge)
        except Exception as err:
            log("error", "purge failed", err=str(err))


def run() -> None:
    import uvicorn

    port = int(env_str("PORT", "8083"))
    log("info", "prediction service starting", port=port, db=env_str("DB_PATH", "data/prediction.db"))
    uvicorn.run(create_app(), host="0.0.0.0", port=port, log_config=None, access_log=False)
