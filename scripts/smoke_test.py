"""End-to-end smoke test, run inside the compose network by scripts/smoke.sh.

    python smoke_test.py pipeline    # the whole system, through the public gateway
    python smoke_test.py degraded    # run while the risk service is stopped
    python smoke_test.py recovered   # run after it has been started again

Standard library only. The weather service must be in simulated mode, so the test
can create a heatwave on demand instead of waiting for one.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

GATEWAY = os.environ.get("GATEWAY_URL", "http://gateway:8080").rstrip("/")
FRONTEND = os.environ.get("FRONTEND_URL", "http://frontend:3000").rstrip("/")
ADMIN_TOKEN = os.environ.get("ADMIN_TOKEN", "")
SINK_HOST, SINK_PORT = "smoke", 9000  # how the other containers reach this one
WEBHOOK_SECRET = "smoke-test-secret"


class SmokeFailure(Exception):
    pass


def step(msg: str) -> None:
    print(f"  ✓ {msg}", flush=True)


def check(cond, msg: str, detail=None) -> None:
    if not cond:
        raise SmokeFailure(f"{msg}" + (f"\n      got: {detail!r}" if detail is not None else ""))
    step(msg)


def http(method: str, url: str, body=None, token: str = "", timeout: float = 10):
    """Returns (status, parsed-JSON-or-text). Never raises for HTTP error statuses."""
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status, raw = resp.status, resp.read()
    except urllib.error.HTTPError as err:
        status, raw = err.code, err.read()
    except (urllib.error.URLError, TimeoutError, ConnectionError) as err:
        return 0, str(err)
    try:
        return status, json.loads(raw)
    except ValueError:
        return status, raw.decode(errors="replace")


def api(method: str, path: str, body=None, admin: bool = False):
    return http(method, f"{GATEWAY}/api/v1{path}", body, ADMIN_TOKEN if admin else "")


def wait_for(fn, what: str, timeout: float = 60, interval: float = 1.0):
    """Poll fn() until it returns something truthy; fail with the last value if it never does."""
    deadline, last = time.time() + timeout, None
    while time.time() < deadline:
        try:
            last = fn()
        except Exception as err:  # a service still starting up is not a failure yet
            last = f"{type(err).__name__}: {err}"
        else:
            if last:
                step(what)
                return last
        time.sleep(interval)
    raise SmokeFailure(f"timed out after {timeout:.0f}s waiting for: {what}\n      last value: {str(last)[:400]}")


# ---- a webhook receiver that verifies signatures ----

class Sink:
    def __init__(self):
        self.events: list[dict] = []
        self.lock = threading.Lock()
        sink = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                want = "sha256=" + hmac.new(WEBHOOK_SECRET.encode(), body, hashlib.sha256).hexdigest()
                payload = json.loads(body)
                with sink.lock:
                    sink.events.append({
                        "event": self.headers.get("X-Heatwave-Event"),
                        "signature_ok": hmac.compare_digest(self.headers.get("X-Heatwave-Signature", ""), want),
                        "payload": payload,
                    })
                self.send_response(200)
                self.end_headers()

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("0.0.0.0", SINK_PORT), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def matching(self, event: str, location_id: int) -> list[dict]:
        with self.lock:
            return [e for e in self.events if e["event"] == event and e["payload"]["alert"]["locationId"] == location_id]


# ---- stages ----

def all_services_ready():
    status, body = api("GET", "/status")
    return body if status == 200 and body.get("status") == "ok" else None


def mumbai_id() -> int:
    status, body = api("GET", "/locations")
    check(status == 200, "GET /api/v1/locations answers 200", (status, body))
    names = {loc["name"]: loc["id"] for loc in body["locations"]}
    check(len(names) >= 8 and "Mumbai" in names, "the 8 starter cities are registered", sorted(names))
    return names["Mumbai"]


def climate(location_id: int):
    status, body = api("GET", f"/locations/{location_id}/climate?hourly=false")
    return status, body


def pipeline() -> None:
    print("pipeline: weather -> processing -> prediction -> risk -> alert, through the gateway")
    check(ADMIN_TOKEN, "ADMIN_TOKEN is set for the test")

    wait_for(all_services_ready, "all five backend services report ready", timeout=120)
    mid = mumbai_id()

    status, page = http("GET", FRONTEND + "/")
    check(status == 200 and "Heatwave" in str(page), "the frontend serves its page", status)
    status, via_frontend = http("GET", FRONTEND + "/api/v1/status")
    check(status == 200 and isinstance(via_frontend, dict) and via_frontend.get("status") == "ok",
          "the frontend proxies /api/v1 to the gateway (what the browser uses)", (status, via_frontend))

    # Security basics at the public door.
    check(api("PUT", "/simulation", {"scenario": "extreme"})[0] == 401, "admin route without a token is refused (401)")
    check(http("PUT", f"{GATEWAY}/api/v1/simulation", {"scenario": "extreme"}, token="wrong")[0] == 401, "admin route with a wrong token is refused (401)")
    check(api("POST", "/subscriptions", {"minLevel": "danger", "channel": "log"})[0] == 401, "creating subscriptions needs the admin token")
    status, body = api("GET", "/no/such/route")
    check(status == 404 and body["error"]["code"] == "not_found", "unknown routes answer with the JSON error shape", body)

    status, sim = api("GET", "/simulation", admin=True)
    check(status == 200 and sim["enabled"] and sim["scenario"] == "normal", "the weather source is the simulator, scenario 'normal'", sim)

    # Quiet weather flows all the way through.
    def settled_normal():
        status, c = climate(mid)
        return c if status == 200 and c.get("status") == "ok" else None
    c = wait_for(settled_normal, "Mumbai's climate is complete (weather, prediction, risk, alerts)", timeout=90)
    status, via_frontend = http("GET", f"{FRONTEND}/api/v1/locations/{mid}/climate")
    check(status == 200 and via_frontend.get("status") == "ok" and via_frontend["weather"]["hourly"],
          "the same composed climate, with its hourly series, arrives through the frontend's proxy", status)
    check(c["risk"]["alertLevel"] == "normal", "normal weather gives a Normal risk level", c["risk"]["alertLevel"])
    check(c["prediction"]["method"] == "model" and c["prediction"]["peak"]["probability"] < 0.05, "the trained model sees no heatwave coming", c["prediction"]["peak"])
    check(c["weather"]["quality"]["score"] >= 0.95, "the cleaned weather data is high quality", c["weather"]["quality"])
    check(c["alerts"] == [] and c["degraded"] is False and set(c["sources"].values()) == {"ok"}, "no alerts, nothing degraded")

    # Create a heatwave and subscribe a signature-verifying webhook.
    sink = Sink()
    status, sub = api("POST", "/subscriptions", {
        "minLevel": "danger", "channel": "webhook", "target": f"http://{SINK_HOST}:{SINK_PORT}/hook",
        "secret": WEBHOOK_SECRET, "label": "smoke test"}, admin=True)
    check(status == 201 and sub["hasSecret"] and "secret" not in sub, "webhook subscription created; the secret is not echoed back", sub)

    status, body = api("PUT", "/simulation", {"scenario": "extreme"}, admin=True)
    check(status == 200 and body["refreshed"] >= 8, "switched every city to an extreme heatwave", body)

    def open_alert():
        status, body = api("GET", f"/alerts?status=open&locationId={mid}")
        return body["alerts"][0] if status == 200 and body["alerts"] else None
    alert = wait_for(open_alert, "an alert opens for Mumbai", timeout=60)
    check(alert["currentLevel"] == "extreme-danger" and alert["headline"].startswith("Extreme Danger heat alert for Mumbai"),
          "the alert is Extreme Danger and says so", alert["headline"])

    opened = wait_for(lambda: sink.matching("alert.opened", mid), "the webhook receives alert.opened", timeout=30)
    check(all(e["signature_ok"] for e in opened), "its HMAC signature verifies")
    check(len(opened) == 1 and opened[0]["payload"]["alert"]["id"] == alert["id"], "exactly one opened notification so far")

    status, c = climate(mid)
    check(status == 200 and c["risk"]["alertLevel"] == "extreme-danger" and len(c["alerts"]) == 1 and c["prediction"]["peak"]["probability"] > 0.9,
          "the composed climate shows the heatwave: risk, probability and the open alert", (c["risk"] or {}).get("alertLevel"))

    # No spam: more readings at the same level notify nobody.
    for _ in range(3):
        check(api("POST", f"/locations/{mid}/refresh", admin=True)[0] == 200, "refreshed Mumbai again (still extreme)")
        time.sleep(1)
    time.sleep(3)
    status, body = api("GET", f"/alerts?status=open&locationId={mid}")
    check(len(body["alerts"]) == 1 and len(sink.matching("alert.opened", mid)) == 1 and not sink.matching("alert.escalated", mid),
          "three more readings at the same level opened no second alert and sent nothing", len(sink.matching("alert.opened", mid)))

    # The heat ends: the alert resolves after the hysteresis window and everyone is told.
    check(api("PUT", "/simulation", {"scenario": "normal"}, admin=True)[0] == 200, "switched back to normal weather")

    def resolved():
        api("POST", f"/locations/{mid}/refresh", admin=True)  # fresh readings drive the resolution
        status, body = api("GET", f"/alerts/{alert['id']}")
        return body if status == 200 and body["status"] == "resolved" else None
    done = wait_for(resolved, "the alert resolves once the danger has passed", timeout=60, interval=2)
    kinds = [e["kind"] for e in done["history"]]
    check(kinds[0] == "opened" and kinds[-1] == "resolved", "its timeline reads opened ... resolved", kinds)
    closed = wait_for(lambda: sink.matching("alert.resolved", mid), "the webhook receives alert.resolved", timeout=30)
    check(all(e["signature_ok"] for e in closed), "the all-clear is signed correctly too")

    status, body = api("GET", "/notifications?status=failed", admin=True)
    check(status == 200 and body["notifications"] == [], "no notification failed permanently", body)
    print("PIPELINE OK")


def degraded() -> None:
    print("degraded: the risk service is stopped")
    mid = mumbai_id()

    def partial():
        status, c = climate(mid)
        return c if status == 200 and c.get("degraded") else None
    c = wait_for(partial, "the climate endpoint still answers, flagged degraded", timeout=30)
    check(c["status"] == "partial" and c["sources"]["risk"] in ("unavailable", "timeout"), "the risk part is reported unavailable", c["sources"])
    check(c["weather"] is not None and c["prediction"] is not None and c["location"]["name"] == "Mumbai",
          "everything else is still served (weather, prediction, location)")

    for _ in range(6):  # enough failures to open the circuit breaker
        climate(mid)
    status, st = api("GET", "/status")
    risk = next(s for s in st["services"] if s["name"] == "risk")
    check(st["status"] == "degraded" and risk["status"] == "down" and risk["circuit"] == "open",
          "status shows risk down with its circuit breaker open", risk)

    started = time.time()
    climate(mid)
    check(time.time() - started < 1.0, "calls fail fast while the circuit is open")
    print("DEGRADED OK")


def recovered() -> None:
    print("recovered: the risk service is back")
    mid = mumbai_id()

    def healthy():
        status, c = climate(mid)
        return c if status == 200 and c.get("status") == "ok" and not c.get("degraded") else None
    wait_for(healthy, "the climate is complete again once the breaker re-probes", timeout=90, interval=2)
    st = wait_for(all_services_ready, "status reports every service ready", timeout=30)
    check(all(s["circuit"] == "closed" for s in st["services"]), "all circuit breakers are closed again")
    print("RECOVERED OK")


STAGES = {"pipeline": pipeline, "degraded": degraded, "recovered": recovered}

if __name__ == "__main__":
    stage = sys.argv[1] if len(sys.argv) > 1 else "pipeline"
    if stage not in STAGES:
        sys.exit(f"unknown stage {stage!r}; use one of {', '.join(STAGES)}")
    try:
        STAGES[stage]()
    except SmokeFailure as err:
        print(f"\n  ✗ SMOKE TEST FAILED: {err}", file=sys.stderr)
        sys.exit(1)
