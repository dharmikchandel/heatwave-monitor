#!/usr/bin/env bash
# Run the whole system locally WITHOUT Docker, for manual testing and debugging.
#
#   scripts/dev.sh start | stop | restart | status | logs [service]
#
# Everything lives in .dev/ (binaries, databases, logs, pids), which is git-ignored.
# Weather is simulated by default so a heatwave can be created on demand; override with
# environment variables, e.g.  WEATHER_SOURCE=openmeteo scripts/dev.sh start
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEV="$ROOT/.dev"
BIN="$DEV/bin"; DATA="$DEV/data"; LOGS="$DEV/logs"; PIDS="$DEV/pids"

# Startup order = pipeline order; the gateway and frontend come last.
SERVICES=(weather processing prediction risk alert user gateway frontend)
GO_SERVICES=(weather processing risk alert user gateway)

port_of() {
  case "$1" in
    weather) echo 8081;; processing) echo 8082;; prediction) echo 8083;; risk) echo 8084;;
    alert) echo 8085;; user) echo 8086;; gateway) echo "${GATEWAY_PORT:-8088}";; frontend) echo "${FRONTEND_PORT:-3000}";;
  esac
}
ready_path() { [ "$1" = frontend ] && echo / || echo /readyz; }

# Defaults for manual testing (all overridable from the environment).
: "${WEATHER_SOURCE:=simulated}" "${SIMULATED_SCENARIO:=normal}"
: "${ADMIN_EMAIL:=admin@heatwave.local}" "${ADMIN_PASSWORD:=dev-admin-password}"   # local use only
: "${RESOLVE_AFTER:=30s}" "${COOLDOWN:=2m}" "${POLL_INTERVAL:=15m}" "${DEFAULT_LOG_SUBSCRIPTION:=true}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

is_up() { # is the process (group leader) for a service alive?
  local f="$PIDS/$1.pid"; [ -f "$f" ] && kill -0 "$(cat "$f")" 2>/dev/null
}

http_code() { curl -s -o /dev/null -m 2 -w '%{http_code}' "http://127.0.0.1:$(port_of "$1")$(ready_path "$1")" 2>/dev/null || true; }

prepare() {
  command -v go >/dev/null || die "go is not installed"
  command -v uv >/dev/null || die "uv is not installed (https://docs.astral.sh/uv/)"
  command -v bun >/dev/null || die "bun is not installed (https://bun.sh)"
  mkdir -p "$BIN" "$DATA" "$LOGS" "$PIDS"
  say "building Go services..."
  for s in "${GO_SERVICES[@]}"; do
    (cd "$ROOT/backend" && go build -o "$BIN/$s" "./cmd/$s")
  done
  say "preparing the Python environment..."
  (cd "$ROOT/backend/prediction" && uv sync --frozen --quiet)
  [ -d "$ROOT/frontend/node_modules" ] || { say "installing frontend dependencies..."; (cd "$ROOT/frontend" && bun install --frozen-lockfile); }
}

start_one() {
  local s="$1" port; port="$(port_of "$s")"
  if is_up "$s"; then say "  $s already running"; return; fi
  if ss -ltn 2>/dev/null | grep -qE "[:.]${port}\s"; then
    die "port $port (needed by $s) is already in use: $(ss -ltnp 2>/dev/null | grep -E "[:.]${port}\s" | head -1 | tr -s ' ')"
  fi

  local -a env_vars=("PORT=$port" "DB_PATH=$DATA/$s.db")
  local -a cmd
  case "$s" in
    weather)    env_vars+=("WEATHER_SOURCE=$WEATHER_SOURCE" "SIMULATED_SCENARIO=$SIMULATED_SCENARIO" "POLL_INTERVAL=$POLL_INTERVAL" "PROCESSING_URL=http://127.0.0.1:8082/internal/events"); cmd=("$BIN/weather");;
    processing) env_vars+=("PREDICTION_URL=http://127.0.0.1:8083/internal/events"); cmd=("$BIN/processing");;
    prediction) env_vars+=("RISK_URL=http://127.0.0.1:8084/internal/events" "PYTHONPATH=$ROOT/backend/prediction/src"); cmd=("$ROOT/backend/prediction/.venv/bin/python" -m prediction);;
    risk)       env_vars+=("ALERT_URL=http://127.0.0.1:8085/internal/events"); cmd=("$BIN/risk");;
    alert)      env_vars+=("RESOLVE_AFTER=$RESOLVE_AFTER" "COOLDOWN=$COOLDOWN" "DEFAULT_LOG_SUBSCRIPTION=$DEFAULT_LOG_SUBSCRIPTION"); cmd=("$BIN/alert");;
    user)       env_vars+=("ADMIN_EMAIL=$ADMIN_EMAIL" "ADMIN_PASSWORD=$ADMIN_PASSWORD"); cmd=("$BIN/user");;
    gateway)    env_vars+=("WEATHER_BASE_URL=http://127.0.0.1:8081" "PROCESSING_BASE_URL=http://127.0.0.1:8082" "PREDICTION_BASE_URL=http://127.0.0.1:8083"
                           "RISK_BASE_URL=http://127.0.0.1:8084" "ALERT_BASE_URL=http://127.0.0.1:8085" "USER_BASE_URL=http://127.0.0.1:8086" "CORS_ORIGINS=http://localhost:$(port_of frontend)"
                           "WRITE_RATE_LIMIT_BURST=${WRITE_RATE_LIMIT_BURST:-50}" "AUTH_RATE_LIMIT_PER_MIN=60" "AUTH_RATE_LIMIT_BURST=30" "TRUST_PROXY=true"); cmd=("$BIN/gateway");;
    frontend)   env_vars+=("GATEWAY_URL=http://127.0.0.1:$(port_of gateway)"); cmd=(bun run dev);;
  esac

  local dir="$ROOT"; [ "$s" = frontend ] && dir="$ROOT/frontend"
  # setsid: give the service its own process group, so `stop` can terminate wrappers (bun) and children together.
  ( cd "$dir" && exec setsid env "${env_vars[@]}" "${cmd[@]}" >>"$LOGS/$s.log" 2>&1 </dev/null ) &
  echo $! >"$PIDS/$s.pid"
  say "  started $s (pid $(cat "$PIDS/$s.pid"), port $port)"
}

wait_ready() {
  local s="$1" i
  for i in $(seq 1 90); do
    is_up "$s" || { say "  $s exited; last log lines:"; tail -n 15 "$LOGS/$s.log" | sed 's/^/    /'; return 1; }
    [ "$(http_code "$s")" = 200 ] && return 0
    sleep 1
  done
  say "  $s did not become ready in 90s; see $LOGS/$s.log"; return 1
}

do_start() {
  prepare
  : >"$LOGS/.keep"
  say "starting services..."
  for s in "${SERVICES[@]}"; do start_one "$s"; done
  say "waiting for readiness..."
  for s in "${SERVICES[@]}"; do wait_ready "$s" && say "  ✓ $s" || { do_stop quiet; exit 1; }; done
  cat <<MSG

The system is running (weather source: $WEATHER_SOURCE).
  frontend  http://localhost:$(port_of frontend)
  api       http://localhost:$(port_of gateway)/api/v1/status
  admin login: $ADMIN_EMAIL / $ADMIN_PASSWORD  (created on first start only)
Try:  make status | make climate ID=1 | make scenario S=extreme | make alerts | make dev-logs
Stop: make dev-stop
MSG
}

do_stop() {
  local quiet="${1:-}" s pid i
  for ((idx=${#SERVICES[@]}-1; idx>=0; idx--)); do
    s="${SERVICES[idx]}"
    if is_up "$s"; then
      pid="$(cat "$PIDS/$s.pid")"
      kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
      for i in 1 2 3 4 5 6 7 8 9 10; do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
      kill -KILL -- "-$pid" 2>/dev/null || true
      [ -n "$quiet" ] || say "  stopped $s"
    fi
    rm -f "$PIDS/$s.pid"
  done
  [ -n "$quiet" ] || say "all stopped (data kept in .dev/data; run 'make dev-clean' to delete it)"
}

do_status() {
  printf '%-11s %-6s %-8s %s\n' SERVICE PORT PID STATE
  for s in "${SERVICES[@]}"; do
    local pid="-" state="stopped"
    if is_up "$s"; then pid="$(cat "$PIDS/$s.pid")"; state="running (HTTP $(http_code "$s"))"; fi
    printf '%-11s %-6s %-8s %s\n' "$s" "$(port_of "$s")" "$pid" "$state"
  done
}

do_logs() {
  mkdir -p "$LOGS"
  if [ -n "${1:-}" ]; then tail -n 80 -F "$LOGS/$1.log"; else tail -n 20 -F "$LOGS"/*.log; fi
}

case "${1:-}" in
  start)   do_start;;
  stop)    do_stop;;
  restart) do_stop quiet; do_start;;
  status)  do_status;;
  logs)    do_logs "${2:-}";;
  *) die "usage: $0 start|stop|restart|status|logs [service]";;
esac
