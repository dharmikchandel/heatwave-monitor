#!/usr/bin/env bash
# Signs in as the administrator and prints a session token, for scripts and curl:
#
#   curl -H "Authorization: Bearer $(scripts/admin_token.sh)" http://localhost:8088/api/v1/notifications
#
# Credentials come from the environment, else from .env (ADMIN_EMAIL / ADMIN_PASSWORD, as
# written by `make env`), else the defaults `make dev` uses. The gateway is GATEWAY, else
# http://localhost:$GATEWAY_PORT (default 8088).
set -euo pipefail
cd "$(dirname "$0")/.."

from_env_file() { [ -f .env ] && grep -E "^$1=" .env | tail -n1 | cut -d= -f2- || true; }

email="${ADMIN_EMAIL:-$(from_env_file ADMIN_EMAIL)}"
password="${ADMIN_PASSWORD:-$(from_env_file ADMIN_PASSWORD)}"
port="${GATEWAY_PORT:-$(from_env_file GATEWAY_PORT)}"
gateway="${GATEWAY:-http://localhost:${port:-8088}}"
: "${email:=admin@heatwave.local}" "${password:=dev-admin-password}"

# jq-free JSON: the values are only ever quoted, and quotes/backslashes are escaped.
esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }
body="{\"email\":\"$(esc "$email")\",\"password\":\"$(esc "$password")\"}"

reply="$(curl -sS -m 15 -X POST "$gateway/api/v1/auth/login" -H 'Content-Type: application/json' -H 'X-Return-Token: 1' -d "$body")" \
  || { echo "could not reach the gateway at $gateway (is the system running?)" >&2; exit 1; }

token="$(printf '%s' "$reply" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
if [ -z "$token" ]; then
  echo "admin sign-in failed for $email: $reply" >&2
  echo "(the administrator is only created on first start; see ADMIN_EMAIL / ADMIN_PASSWORD in .env)" >&2
  exit 1
fi
printf '%s\n' "$token"
