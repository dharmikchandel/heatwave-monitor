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
ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "")
PASSWORD = "correct-horse-battery-staple"  # for the accounts the test registers
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


def http(method: str, url: str, body=None, token: str = "", timeout: float = 10, headers=None, with_headers: bool = False):
    """Returns (status, parsed-JSON-or-text), plus the response headers when asked.
    Never raises for HTTP error statuses."""
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    for name, value in (headers or {}).items():
        req.add_header(name, value)
    got = {}
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status, raw, got = resp.status, resp.read(), resp.headers
    except urllib.error.HTTPError as err:
        status, raw, got = err.code, err.read(), err.headers
    except (urllib.error.URLError, TimeoutError, ConnectionError) as err:
        return (0, str(err), {}) if with_headers else (0, str(err))
    try:
        parsed = json.loads(raw)
    except ValueError:
        parsed = raw.decode(errors="replace")
    return (status, parsed, got) if with_headers else (status, parsed)


def api(method: str, path: str, body=None, token: str = ""):
    return http(method, f"{GATEWAY}/api/v1{path}", body, token)


def sign_up(email: str, password: str = PASSWORD, name: str = "Smoke Tester") -> str:
    """Registers an account and returns its session token (asked for with X-Return-Token)."""
    status, body = http("POST", f"{GATEWAY}/api/v1/auth/register", {"email": email, "password": password, "displayName": name},
                        headers={"X-Return-Token": "1"})
    check(status == 201 and body.get("token"), f"registered {email}", (status, body))
    return body["token"]


def sign_in(email: str, password: str) -> tuple[int, dict]:
    return http("POST", f"{GATEWAY}/api/v1/auth/login", {"email": email, "password": password}, headers={"X-Return-Token": "1"})


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


# ---- accounts ----

def admin_session() -> str:
    check(ADMIN_EMAIL and ADMIN_PASSWORD, "the administrator's credentials are set for the test")
    status, body = sign_in(ADMIN_EMAIL, ADMIN_PASSWORD)
    check(status == 200 and body.get("token") and body["user"]["role"] == "admin", "the seeded administrator can sign in", (status, body))
    return body["token"]


def inbox(token: str) -> dict:
    status, body = api("GET", "/me/notifications", token=token)
    if status != 200:
        raise SmokeFailure(f"GET /me/notifications should answer 200\n      got: {(status, body)!r}")
    return body


def accounts_basics(admin: str, mid: int) -> dict:
    """Registration rules, sign-in, the cookie that browsers use, and what ordinary users may not do."""
    print("accounts:")
    register = f"{GATEWAY}/api/v1/auth/register"
    ada = sign_up("ada@example.org")
    bob = sign_up("bob@example.org")
    cara = sign_up("cara@example.org")

    status, body = http("POST", register, {"email": "not-an-email", "password": PASSWORD})
    check(status == 400 and body["error"]["code"] == "validation_failed", "a malformed email is refused", (status, body))
    status, body = http("POST", register, {"email": "weak@example.org", "password": "short"})
    check(status == 400 and body["error"]["code"] == "validation_failed", "a weak password is refused", (status, body))
    status, body = http("POST", register, {"email": "ADA@Example.org", "password": PASSWORD})
    check(status == 409 and body["error"]["code"] == "email_taken", "the same email in other letters is already taken", (status, body))
    status, body = http("POST", register, {"email": "x@example.org", "password": PASSWORD, "role": "admin"})
    check(status == 400, "a client cannot ask for the admin role at registration", (status, body))

    s1, b1 = sign_in("ada@example.org", "not-the-password-1")
    s2, b2 = sign_in("nobody@example.org", "not-the-password-1")
    check(s1 == s2 == 401 and b1["error"]["code"] == b2["error"]["code"] == "invalid_credentials" and b1["error"]["message"] == b2["error"]["message"],
          "a wrong password and an unknown email get the same answer", (b1, b2))
    check(api("GET", "/me/watchlist")[0] == 401, "private routes need a session")

    check(api("GET", "/subscriptions", token=ada)[0] == 403, "an ordinary user cannot read operator subscriptions (403)")
    check(api("PUT", "/simulation", {"scenario": "extreme"}, token=ada)[0] == 403, "an ordinary user cannot change the simulation (403)")
    check(api("GET", "/admin/users", token=ada)[0] == 403, "an ordinary user cannot list accounts (403)")

    # The browser's way in: a cookie, set through the frontend's proxy, never visible to scripts.
    status, body, headers = http("POST", f"{FRONTEND}/api/v1/auth/register", {"email": "cookie@example.org", "password": PASSWORD},
                                 with_headers=True)
    cookies = headers.get_all("Set-Cookie") or []
    cookie = next((c for c in cookies if c.startswith("hm_session=")), "")
    check(status == 201 and cookie and "HttpOnly" in cookie and "SameSite=Lax" in cookie and "token" not in body,
          "registering through the frontend sets an HttpOnly, SameSite cookie and no token in the body", (status, cookies, body))
    pair = cookie.split(";")[0]
    status, body = http("GET", f"{FRONTEND}/api/v1/auth/session", headers={"Cookie": pair})
    check(status == 200 and body["user"]["email"] == "cookie@example.org", "that cookie identifies the user", (status, body))
    status, body = http("POST", f"{FRONTEND}/api/v1/me/subscriptions", {"locationId": mid, "minLevel": "danger"},
                        headers={"Cookie": pair, "Sec-Fetch-Site": "cross-site"})
    check(status == 403 and body["error"]["code"] == "cross_site_request", "a cross-site write with the cookie is refused", (status, body))
    status, _ = http("POST", f"{FRONTEND}/api/v1/auth/logout", headers={"Cookie": pair})
    check(status == 204, "signing out answers 204")
    status, body = http("GET", f"{FRONTEND}/api/v1/auth/session", headers={"Cookie": pair})
    check(status == 200 and body["user"] is None, "the old cookie no longer identifies anyone", (status, body))

    # Guessing a password locks that email out from that address, then lifts.
    sign_up("lock@example.org")
    for _ in range(5):
        check(sign_in("lock@example.org", "not-the-password-1")[0] == 401, "wrong password refused")
    status, body, headers = http("POST", f"{GATEWAY}/api/v1/auth/login", {"email": "lock@example.org", "password": PASSWORD}, with_headers=True)
    check(status == 429 and body["error"]["code"] == "too_many_attempts" and int(headers.get("Retry-After", "0")) > 0,
          "after five failures even the right password is refused for a while (429 with Retry-After)", (status, body))
    return {"ada": ada, "bob": bob, "cara": cara}


def follow_mumbai(users: dict, mid: int) -> None:
    city = {"name": "Mumbai", "country": "India", "latitude": 19.08, "longitude": 72.88, "timezone": "Asia/Kolkata"}
    check(api("PUT", f"/me/watchlist/{mid}", city, token=users["ada"])[0] == 204, "ada saves Mumbai to her watchlist")
    status, body = api("GET", "/me/watchlist", token=users["ada"])
    check(status == 200 and [c["locationId"] for c in body["cities"]] == [mid], "her watchlist shows it", body)
    check(api("GET", "/me/watchlist", token=users["bob"])[1]["cities"] == [], "and nobody else's does")
    status, sub = api("POST", "/me/subscriptions", {"locationId": mid, "minLevel": "danger"}, token=users["ada"])
    check(status == 200 and sub["channel"] == "inapp", "ada wants in-app alerts for Mumbai from Danger up", (status, sub))
    check(api("POST", "/me/subscriptions", {"locationId": mid, "minLevel": "extreme-danger"}, token=users["bob"])[0] == 200,
          "bob only wants the worst (Extreme Danger)")
    status, body = api("POST", "/me/subscriptions", {"locationId": mid, "minLevel": "danger", "channel": "webhook", "target": "http://example.org"}, token=users["cara"])
    check(status == 400, "a user cannot pick another channel or a webhook target", (status, body))
    check(inbox(users["ada"])["notifications"] == [], "the inbox is empty while all is calm")


def inbox_after_opening(users: dict) -> None:
    box = wait_for(lambda: (b := inbox(users["ada"]))["unread"] == 1 and b, "an in-app notification reaches ada's inbox", timeout=30)
    first = box["notifications"][0]
    check(first["kind"] == "opened" and first["locationName"] == "Mumbai" and first["level"] == "extreme-danger" and first["headline"],
          "it says what opened, where, and how bad", first)
    check(inbox(users["bob"])["unread"] == 1, "bob (Extreme Danger and up) is told too")
    check(inbox(users["cara"])["notifications"] == [], "cara, who follows nothing, is not told")
    check(api("POST", f"/me/notifications/{first['id']}/read", token=users["bob"])[0] == 404, "bob cannot mark ada's notification read (404)")
    check(api("POST", f"/me/notifications/{first['id']}/read", token=users["ada"])[0] == 204, "ada marks hers read")
    check(inbox(users["ada"])["unread"] == 0, "her unread count drops to zero")


def inbox_after_resolution(users: dict) -> None:
    box = wait_for(lambda: (b := inbox(users["ada"]))["unread"] == 1 and b, "ada is told when the alert resolves", timeout=30)
    check([n["kind"] for n in box["notifications"]] == ["resolved", "opened"], "her inbox reads resolved, opened (newest first)", box["notifications"])
    check(api("POST", "/me/notifications/read", token=users["ada"])[0] == 204, "she marks everything read")
    check(inbox(users["ada"])["unread"] == 0, "nothing is unread")


def inapp_notifications(admin: str) -> int:
    status, body = api("GET", "/notifications?limit=500", token=admin)
    check(status == 200, "the operator's notification list answers 200", (status, body))
    return sum(1 for n in body["notifications"] if n["channel"] == "inapp")


def account_management(admin: str, users: dict) -> None:
    status, body = api("GET", "/admin/users", token=admin)
    emails = {u["email"] for u in body["users"]} if status == 200 else set()
    check({"ada@example.org", "bob@example.org", ADMIN_EMAIL} <= emails, "the administrator sees every account", sorted(emails))
    check(all("password" not in u and "passwordHash" not in u for u in body["users"]), "and none of them exposes a password hash")
    bob_id = next(u["id"] for u in body["users"] if u["email"] == "bob@example.org")
    admin_id = next(u["id"] for u in body["users"] if u["email"] == ADMIN_EMAIL.lower())

    check(api("POST", f"/admin/users/{admin_id}/disable", token=admin)[0] == 409, "the administrator cannot disable themselves")
    check(api("POST", f"/admin/users/{bob_id}/disable", token=admin)[0] == 200, "the administrator disables bob")
    check(api("GET", "/me/watchlist", token=users["bob"])[0] == 401, "bob's open session stops working at once")
    status, body = sign_in("bob@example.org", PASSWORD)
    check(status == 403 and body["error"]["code"] == "account_disabled", "bob cannot sign in (403 account_disabled)", (status, body))
    check(api("POST", f"/admin/users/{bob_id}/enable", token=admin)[0] == 200, "the administrator enables bob again")
    check(sign_in("bob@example.org", PASSWORD)[0] == 200, "and bob can sign in")

    # Changing a password ends the other sessions.
    ada = users["ada"]
    status, body = http("POST", f"{GATEWAY}/api/v1/auth/password", {"current": PASSWORD, "new": "another-long-passphrase-9"},
                        token=ada, headers={"X-Return-Token": "1"})
    check(status == 200 and body.get("token") and body["token"] != ada, "ada changes her password and gets a fresh session", (status, body))
    check(api("GET", "/me/watchlist", token=ada)[0] == 401, "her old session is gone")
    check(sign_in("ada@example.org", PASSWORD)[0] == 401, "the old password no longer works")
    ada = body["token"]

    # Deleting an account takes its data with it.
    before = inapp_notifications(admin)
    check(api("DELETE", "/auth/account", {"password": "wrong-password-123"}, token=ada)[0] == 401, "deleting needs the right password")
    check(api("GET", "/me/watchlist", token=ada)[0] == 200, "a wrong password deletes nothing")
    check(api("DELETE", "/auth/account", {"password": "another-long-passphrase-9"}, token=ada)[0] == 204, "ada deletes her account")
    check(api("GET", "/me/watchlist", token=ada)[0] == 401, "her session is gone")
    check(sign_in("ada@example.org", "another-long-passphrase-9")[0] == 401, "her account is gone")
    after = inapp_notifications(admin)
    check(after == before - 2, "her two in-app notifications were deleted with her", (before, after))
    status, body = api("GET", "/admin/users", token=admin)
    check("ada@example.org" not in {u["email"] for u in body["users"]}, "she no longer appears in the account list")
    print("ACCOUNTS OK")


def pipeline() -> None:
    print("pipeline: weather -> processing -> prediction -> risk -> alert, through the gateway")
    wait_for(all_services_ready, "all six backend services report ready", timeout=120)
    mid = mumbai_id()

    status, page = http("GET", FRONTEND + "/")
    check(status == 200 and "Heatwave" in str(page), "the frontend serves its page", status)
    for path, heading in (("/alerts", "Heat Alerts"), ("/status", "System Status")):
        status, page = http("GET", FRONTEND + path)
        check(status == 200 and heading in str(page), f"the frontend serves its {path} page", status)
    for path, title in (("/login", "Sign in"), ("/register", "Create an account"), ("/account", "Your account"), ("/inbox", "Inbox"), ("/admin", "Administration")):
        status, page = http("GET", FRONTEND + path)
        check(status == 200 and f"{title} — Heatwave Monitor" in str(page), f"the frontend serves its {path} page", status)
    status, via_frontend = http("GET", FRONTEND + "/api/v1/status")
    check(status == 200 and isinstance(via_frontend, dict) and via_frontend.get("status") == "ok",
          "the frontend proxies /api/v1 to the gateway (what the browser uses)", (status, via_frontend))

    admin = admin_session()

    # Security basics at the public door.
    check(api("PUT", "/simulation", {"scenario": "extreme"})[0] == 401, "admin route without a session is refused (401)")
    check(api("PUT", "/simulation", {"scenario": "extreme"}, token="wrong")[0] == 401, "admin route with a wrong token is refused (401)")
    check(api("POST", "/subscriptions", {"minLevel": "danger", "channel": "log"})[0] == 401, "creating operator subscriptions needs an administrator")
    check(api("POST", "/locations", {"name": "Nowhere", "latitude": 1, "longitude": 1})[0] == 401, "adding a city needs an account")
    status, body = api("GET", "/no/such/route")
    check(status == 404 and body["error"]["code"] == "not_found", "unknown routes answer with the JSON error shape", body)

    users = accounts_basics(admin, mid)

    status, sim = api("GET", "/simulation", token=admin)
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
    follow_mumbai(users, mid)
    sink = Sink()
    status, sub = api("POST", "/subscriptions", {
        "minLevel": "danger", "channel": "webhook", "target": f"http://{SINK_HOST}:{SINK_PORT}/hook",
        "secret": WEBHOOK_SECRET, "label": "smoke test"}, token=admin)
    check(status == 201 and sub["hasSecret"] and "secret" not in sub, "webhook subscription created; the secret is not echoed back", sub)

    status, body = api("PUT", "/simulation", {"scenario": "extreme"}, token=admin)
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

    inbox_after_opening(users)

    status, c = climate(mid)
    check(status == 200 and c["risk"]["alertLevel"] == "extreme-danger" and len(c["alerts"]) == 1 and c["prediction"]["peak"]["probability"] > 0.9,
          "the composed climate shows the heatwave: risk, probability and the open alert", (c["risk"] or {}).get("alertLevel"))

    # No spam: more readings at the same level notify nobody.
    for _ in range(3):
        check(api("POST", f"/locations/{mid}/refresh", token=admin)[0] == 200, "refreshed Mumbai again (still extreme)")
        time.sleep(1)
    time.sleep(3)
    status, body = api("GET", f"/alerts?status=open&locationId={mid}")
    check(len(body["alerts"]) == 1 and len(sink.matching("alert.opened", mid)) == 1 and not sink.matching("alert.escalated", mid),
          "three more readings at the same level opened no second alert and sent nothing", len(sink.matching("alert.opened", mid)))

    # The heat ends: the alert resolves after the hysteresis window and everyone is told.
    check(api("PUT", "/simulation", {"scenario": "normal"}, token=admin)[0] == 200, "switched back to normal weather")

    def resolved():
        api("POST", f"/locations/{mid}/refresh", token=admin)  # fresh readings drive the resolution
        status, body = api("GET", f"/alerts/{alert['id']}")
        return body if status == 200 and body["status"] == "resolved" else None
    done = wait_for(resolved, "the alert resolves once the danger has passed", timeout=60, interval=2)
    kinds = [e["kind"] for e in done["history"]]
    check(kinds[0] == "opened" and kinds[-1] == "resolved", "its timeline reads opened ... resolved", kinds)
    closed = wait_for(lambda: sink.matching("alert.resolved", mid), "the webhook receives alert.resolved", timeout=30)
    check(all(e["signature_ok"] for e in closed), "the all-clear is signed correctly too")

    status, body = api("GET", "/notifications?status=failed", token=admin)
    check(status == 200 and body["notifications"] == [], "no notification failed permanently", body)

    inbox_after_resolution(users)
    account_management(admin, users)
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
