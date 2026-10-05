# Heatwave Monitor

**Heatwave monitoring, prediction and early warning: a live dashboard on top of a small microservices backend.**

Heatwave Monitor fetches live weather for any city, estimates the chance of a heatwave warning on each of the next seven days with a trained model, explains the resulting risk level in plain language, opens and resolves alerts, and tells the people who follow a city. People can create accounts, save cities and receive alerts in an in-app inbox; administrators manage accounts and the system.

The backend is optional. Without it the dashboard still works: it fetches Open-Meteo directly and computes everything in the browser, exactly as the first version did. With it you get cleaned data, probabilities, reasoned risk levels, alerts and accounts.

> Next.js 16 · TypeScript · Tailwind CSS v4 · Go · Python (FastAPI) · SQLite · Docker Compose

---

## Contents

- [Quick start](#quick-start)
- [What it does](#what-it-does)
- [Architecture](#architecture)
- [Accounts and security](#accounts-and-security)
- [API](#api)
- [Configuration](#configuration)
- [The prediction model](#the-prediction-model)
- [Risk levels](#risk-levels)
- [Accessibility](#accessibility)
- [Testing](#testing)
- [Project structure](#project-structure)
- [Deployment](#deployment)
- [Limitations](#limitations)
- [Data source and attribution](#data-source-and-attribution)

---

## Quick start

Everything is driven by `make`; `make help` lists every command.

**With Docker** (the whole system, eight containers):

```bash
make demo     # simulated weather, so you can create a heatwave on demand
# or
make up       # live Open-Meteo weather
```

Then open <http://localhost:3000>. The first run creates `.env` with a random administrator password (`make env` does only that); the login is printed once and kept in `.env`.

**Without Docker** (needs Go, [uv](https://docs.astral.sh/uv/) and [Bun](https://bun.sh)):

```bash
make dev      # builds and starts all eight processes locally, simulated weather
make dev-stop
```

**Frontend only**, no backend at all (local mode):

```bash
cd frontend && bun install && bun run dev
```

### Try it

With `make demo` (or `make dev`) running:

1. Open the app and **Create an account**. Search for **Mumbai** (one of the eight starter cities), then press **Save city** and **Alerts off** (it becomes **Alerts on**). Adding a city the backend does not know yet needs an account too; until you sign in, such cities are shown in local mode.
2. Start a heatwave: `make scenario S=extreme`. Reload the dashboard: Mumbai is at Extreme Danger with a high heatwave probability, an alert is open on **Alerts**, and within a minute the bell in the header shows an unread notification, which is waiting in your **Inbox**.
3. End it: `make scenario S=normal`. The alert resolves after a short hysteresis window (30 seconds under `make demo`) and you get an all-clear.
4. Sign in as the administrator (`ADMIN_EMAIL` / `ADMIN_PASSWORD` in `.env`; `admin@heatwave.local` / `dev-admin-password` under `make dev`) to see **Administration**, where accounts can be disabled and enabled.
5. Break something: `docker compose -f deploy/docker-compose.yml --project-directory . stop risk`. The dashboard keeps working with the risk part marked unavailable, **Status** shows the circuit breaker open, and it all recovers when the service returns.

Handy helpers: `make status`, `make cities`, `make climate ID=1`, `make alerts`, `make users`, `make notifications`, `make logs SVC=risk`.

## What it does

**Dashboard.** Current conditions with an animated heat-risk gauge, an early-warning banner, an hourly and a seven-day chart, per-day risk cards (with the heatwave probability when the backend is connected), and safety advice for the current tier. Search any city or use your location; switch °C/°F and light/dark; export a report as an image or text.

**Forecast, Safety, About.** A trend and hourly-detail view, a standalone heat-safety guide (every tier, heat-illness stages, emergencies) and the methodology.

**Alerts and Status.** Every watched city is assessed continuously. An alert opens when a Danger-level heatwave is under way or forecast, is updated as it escalates, and resolves once the danger has passed, with its reasoning and who was notified. **Status** shows each service's health and circuit breaker, plus the model's evaluation.

**Accounts.** Register and sign in; save cities; choose per city whether to be told at Danger or only at Extreme Danger; read alerts in an inbox with an unread badge; change your password; delete your account (which deletes your data). Administrators list accounts and disable or re-enable them.

A badge on the dashboard always says where the numbers came from (backend or local), and why when it had to fall back.

## Architecture

```text
Browser ─► Next.js :3000 ──/api/v1/*──► API gateway :8080 ─┬─► weather :8081 ──event──► processing :8082
          (proxy.ts rewrites)           sessions, limits,   │                              │ event
                                        breakers, composes  │                              ▼
                                                            │       alert :8085 ◄─event─ risk :8084 ◄─event─ prediction :8083 (Python)
                                                            └─► user :8086 (accounts, sessions, watchlists)
```

| Service | Language | Owns |
|---|---|---|
| **weather** | Go | The watched locations; polls Open-Meteo (or a scripted simulator) and emits raw observations |
| **processing** | Go | Repairs gaps and outliers, computes heat index and daily metrics, scores data quality |
| **prediction** | Python / FastAPI | Probability of a heatwave-warning day for each of the next seven days (trained model, rule-based fallback) |
| **risk** | Go | Turns probabilities and temperatures into a risk level with reasons |
| **alert** | Go | Alert episodes (open, escalate, resolve), subscriptions, in-app inbox, webhook and log notifications |
| **user** | Go | Accounts, sessions, watchlists |
| **gateway** | Go | The one public door: routing, sessions, rate limits, circuit breakers, the composed climate endpoint |
| **frontend** | Next.js | The app; `proxy.ts` forwards `/api/v1/*` to the gateway at runtime (`GATEWAY_URL`) |

Design choices worth knowing:

- **One SQLite file per service**, on its own volume. No database containers, and no service reads another's data.
- **Events without a broker.** Each service writes its outgoing event to an *outbox* table in the same transaction as its data; a dispatcher pushes it to the next service over HTTP with retries. Receivers keep an *inbox* of event ids so redelivery is harmless. Delivery is at-least-once and ordered, and a stopped service simply catches up when it returns. Bad data is recorded in a `rejections` table and consumed, never retried forever; stale or out-of-order readings are dropped.
- **Resilience at the gateway.** Per-service timeouts and circuit breakers, a composed `/locations/{id}/climate` answer that degrades gracefully (weather without risk if risk is down), token-bucket rate limits (stricter for writes and for sign-in), and JSON errors with request ids throughout.
- **Hardened containers.** Read-only root filesystem, all capabilities dropped, no new privileges, non-root user, memory limits; only the frontend and gateway are published, and only on localhost.
- **Shared contracts.** Go and Python read and write the same fixtures in `testdata/`, and the Go gateway's output is parsed by the frontend's tests, so the services cannot drift apart unnoticed.

## Accounts and security

- **Passwords** are hashed with argon2id (the OWASP minimum profile), with a cap on concurrent hashes so a burst of sign-ins cannot exhaust memory. A password must be 10 to 128 characters and not on a list of common ones. Unknown emails take as long to refuse as known ones, and a wrong password and an unknown email give the same answer.
- **Sessions** are random 256-bit tokens, stored only as a hash, valid for seven days (`SESSION_TTL`), at most ten per account. The browser holds the token in an `HttpOnly`, `SameSite=Lax` cookie (`Secure` over https), so page scripts can never read it. Changing the password ends every other session; disabling or deleting an account ends all of them at once.
- **Brute force.** Five wrong passwords lock that email from that address for 15 minutes (so an attacker locks themselves out, not the owner), on top of the gateway's own per-address limits.
- **Cross-site requests.** A write that carries the session cookie and is marked cross-site by the browser (`Sec-Fetch-Site`) is refused, in addition to `SameSite`. CORS is closed by default.
- **Roles.** `user` and `admin`. The first administrator is created from `ADMIN_EMAIL` / `ADMIN_PASSWORD` only when none exists. The last administrator can neither be disabled nor deleted, and nobody can disable themselves.
- **Trust model.** Only the gateway talks to the internal services, and it tells them who the caller is with `X-User-ID` / `X-User-Role` / `X-Client-IP` headers, which it builds from scratch for every call: a client's own copies, cookies and `Authorization` header never reach a backend. Those headers are trustworthy only because the services are reachable solely from the private network, which is how the compose file and the container hardening are set up. **Do not publish the internal ports.**
- **Command-line access.** Send `X-Return-Token: 1` to `/auth/login` to get the token in the response, then use `Authorization: Bearer <token>`. `scripts/admin_token.sh` does exactly that for the administrator; the `make` helpers use it.

## API

Base path `/api/v1`, JSON, errors shaped `{"error": {"code", "message"}, "request_id"}`. Access: **public**, **user** (signed in), **admin**.

| Access | Routes |
|---|---|
| public | `GET /locations`, `/locations/{id}`, `/locations/{id}/climate` (weather + prediction + risk + alerts, composed), `/alerts`, `/alerts/{id}`, `/model`, `/status` |
| accounts | `POST /auth/register`, `/auth/login`, `/auth/logout` · `GET /auth/session` · `POST /auth/password` · `DELETE /auth/account` |
| user | `POST /locations` · `GET/PUT/DELETE /me/watchlist[/{id}]` · `GET/POST/DELETE /me/subscriptions[/{id}]` · `GET /me/notifications` · `POST /me/notifications/read`, `/me/notifications/{id}/read` |
| admin | `DELETE /locations/{id}` · `POST /locations/{id}/refresh` · `GET/PUT /simulation` · `POST /alerts/{id}/ack` · `GET/POST/DELETE /subscriptions[/{id}]` (webhook and log) · `GET /notifications` · `GET /rejections/{service}` · `GET /admin/users` · `POST /admin/users/{id}/disable`, `/enable` |

Operators can also subscribe a webhook (HMAC-signed with a secret, retried with backoff, restricted to `WEBHOOK_ALLOWED_HOSTS` when set). Every service exposes `/healthz`, `/readyz` and Prometheus-format `/metrics`.

## Configuration

Every setting has a default; `.env.example` documents them all and `make env` turns it into `.env`. The ones you are most likely to touch:

| Variable | Meaning | Default |
|---|---|---|
| `WEATHER_SOURCE` | `openmeteo` (live) or `simulated` (scripted scenarios) | `openmeteo` |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | the first administrator (created only if none exists) | random password from `make env` |
| `OPEN_LEVEL` | lowest level that opens an alert | `danger` |
| `RESOLVE_AFTER`, `COOLDOWN` | how long risk must stay low to resolve; window in which an alert reopens quietly | `1h`, `2h` |
| `SESSION_TTL` | how long a sign-in lasts | `168h` |
| `COOKIE_SECURE` | mark the session cookie `Secure` (set when served over https) | `false` |
| `TRUST_PROXY` | take client addresses from `X-Forwarded-For` (the frontend proxies to the gateway) | `true` |
| `GATEWAY_PORT`, `FRONTEND_PORT` | host ports, bound to localhost | `8088`, `3000` |

The frontend reads `GATEWAY_URL` at runtime; unset, it runs in local mode.

## The prediction model

The target is the app's own warning rule: a day is a heatwave-warning day when its apparent temperature reaches 54 °C, or 41 °C on two consecutive days. For each lead time from 0 to 6 days there is a logistic regression over six temperature features (apparent maximum today and on the two previous days, air maximum and minimum, and the gap between them), expanded with degree-2 interactions. It was trained on ERA5 reanalysis for 37 hot-climate and comparison cities (1991 to 2024, about 3.2 million city-days, base rate 5.8 %), with synthetic forecast noise that grows with lead time so the probabilities are honest about uncertainty, and recent years weighted more.

On held-out years (2020 to 2024) the ROC AUC is 0.997 at lead 0 and 0.984 at lead 6, with a Brier score of 0.010 to 0.023 against 0.059 for always predicting the climatological rate; leave-one-city-out AUC stays between 0.991 and 0.998. These numbers are in `model.json` and are shown on **Status**.

Inference is plain Python (`sigmoid(w·x + b)`), so the service image has no ML dependencies. If `model.json` is missing the service falls back to a rule-based estimate and says so. Retrain with `make train` (downloads weather history; slow); training code is in `scripts/train/`.

## Risk levels

Apparent temperature is classified against WMO-aligned thresholds, using the NWS (Rothfusz) heat index for "feels like":

| Level | Threshold |
|---|---|
| Normal | below 32 °C |
| Caution | 32 °C and above |
| Extreme Caution | 38 °C and above |
| Danger | 41 °C and above on two or more consecutive days |
| Extreme Danger | 54 °C and above |

A single hot day is only Extreme Caution: Danger needs persistence, which is what makes it a heatwave detector rather than a thermometer. The rules live in `frontend/lib/heatwaveEngine.ts` and in the Go `engine` package, and a shared set of test vectors generated from the TypeScript version keeps the two identical.

## Accessibility

The city search is a proper ARIA combobox (arrow keys, Enter, Escape) and the export dialog traps and restores focus. Every interactive element shares one `:focus-visible` ring; forms have labelled fields with the right `autocomplete` values, errors are announced, and toggles expose their state with `aria-pressed`. Risk colours meet WCAG AA contrast in both themes, charts and gauges carry text summaries, and `prefers-reduced-motion` switches off looping animation.

## Testing

```bash
make test     # Go (vet + race detector), Python, and the frontend (tests, lint, types)
make smoke    # end to end in Docker
```

`make smoke` builds everything, starts it with simulated weather in its own compose project, and runs three stages: the **pipeline** (a heatwave through every service to a signed webhook and an in-app inbox, accounts, roles, lock-out, cookies through the frontend proxy, account deletion), then **degraded** (one service stopped: the rest keep answering and the circuit opens) and **recovered**. It takes a few minutes the first time.

The unit tests are written to fail when behaviour breaks: security-relevant ones (role checks, ownership, cross-site protection, header spoofing, redirects, token handling) were each checked by deliberately breaking the code and watching a test fail.

## Project structure

```text
frontend/     Next.js app: app/ (routes), components/, lib/ (engine, backend client, auth)
backend/      Go module: cmd/ (one binary per service), internal/ (service code, shared packages)
  prediction/   the Python service
deploy/       docker-compose.yml
scripts/      smoke test, local runner (dev.sh), admin_token.sh, model training (train/)
testdata/     fixtures shared by the Go, Python and TypeScript tests
Makefile      every run, poke and test command
.env.example  every setting, documented
```

## Deployment

**Docker Compose** is the supported way to run the whole system (`make up`). Compose passes `.env` through; images are tagged `heatwave-monitor/<service>:<TAG>`. Put a TLS-terminating proxy in front of the frontend for anything shared, and set `COOKIE_SECURE=true`.

**Vercel** hosts the frontend alone, in local mode (no backend): import the repository and set the project's **Root Directory** to `frontend`. To connect it to a backend, set `GATEWAY_URL` to the gateway's origin and make sure it is reachable from Vercel; the browser still only ever talks to the frontend's own origin.

CI/CD and Kubernetes manifests are not part of this repository yet.

## Limitations

- **SQLite means one replica per stateful service.** Each service owns a file that only one process may write, so none of them scales horizontally; that is the trade for needing no database server. Moving a service to a networked database later is a local change inside that service.
- **State kept in memory is lost on restart:** the login lock-out counters, the gateway's rate-limit buckets and its short session cache. Restarting forgives a lock-out; it never grants access.
- **Delivery is at-least-once.** Receivers deduplicate by event id, and webhooks carry a delivery id so subscribers can too.
- **The internal network is the security boundary** for the identity headers (see the trust model). Anyone who can reach a backend port directly can impersonate any user.
- **No email, SMS or push.** Notifications go to the in-app inbox, signed webhooks and the log.
- **Registration reveals whether an email is taken** (it has to say so), which is limited only by rate limiting, and there is **no password reset or email verification.** Registration only needs an email address nobody has used; an administrator can disable an account but not set its password.
- **Weather is a single provider** (Open-Meteo). If it is down the backend keeps serving the last readings it has, and local mode has nothing to fall back to.

## Data source and attribution

Weather and geocoding come from the [Open-Meteo API](https://open-meteo.com) (free, non-commercial licence); training data is ERA5 reanalysis served by Open-Meteo's archive. Open-Meteo supplies raw meteorology only: heat index, probabilities, risk levels and alerts are computed by this project.

Built by dharmikchandel.
