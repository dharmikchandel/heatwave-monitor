#!/usr/bin/env bash
# Builds the whole system, starts it in simulated-weather mode and runs the
# end-to-end smoke test, including an outage of one service and its recovery.
#
#   scripts/smoke.sh                 # build, run, tear down
#   SMOKE_NO_BUILD=1 scripts/smoke.sh    # reuse images that are already built
#   SMOKE_KEEP=1 scripts/smoke.sh        # leave the stack running afterwards
#
# Needs only Docker (with the compose plugin).
set -euo pipefail
cd "$(dirname "$0")/.."

# A separate compose project, volumes and ports, so a running dev stack is untouched.
export COMPOSE_PROJECT_NAME=heatwave-smoke
export WEATHER_SOURCE=simulated SIMULATED_SCENARIO=normal
export ADMIN_TOKEN=smoke-admin-token
export RESOLVE_AFTER=3s COOLDOWN=10s POLL_INTERVAL=1h
export WRITE_RATE_LIMIT_PER_MIN=600 WRITE_RATE_LIMIT_BURST=100
export GATEWAY_PORT=18080 FRONTEND_PORT=13000
export REVISION="${REVISION:-smoke}"

compose() { docker compose -f deploy/docker-compose.yml --project-directory . --profile smoke "$@"; }
# --use-aliases: `run` containers otherwise get no DNS name, and the smoke test hosts a webhook receiver.
run_stage() { compose run --rm --no-deps --use-aliases smoke python /smoke_test.py "$1"; }

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo; echo "==== smoke test failed (exit $status): recent service logs ===="
    compose logs --no-color --tail=40 weather processing prediction risk alert gateway 2>/dev/null || true
  fi
  if [ -z "${SMOKE_KEEP:-}" ]; then
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  else
    echo "stack left running (project $COMPOSE_PROJECT_NAME); stop it with: docker compose -p $COMPOSE_PROJECT_NAME down -v"
  fi
  exit "$status"
}
trap cleanup EXIT

if [ -z "${SMOKE_NO_BUILD:-}" ]; then
  echo "==> building images"
  compose build
fi

echo "==> starting the system"
compose down --volumes --remove-orphans >/dev/null 2>&1 || true
compose up -d --wait --wait-timeout 180 gateway frontend

echo "==> stage 1/3: the whole pipeline"
run_stage pipeline

echo "==> stage 2/3: stop the risk service"
compose stop risk
run_stage degraded

echo "==> stage 3/3: start it again"
compose start risk
run_stage recovered

echo; echo "ALL SMOKE STAGES PASSED"
