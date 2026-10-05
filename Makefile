# Heatwave Monitor: every command for running, exploring and testing the system.
# `make help` lists them. Settings come from .env (copy .env.example) when it exists.

SHELL := /bin/bash
-include .env

COMPOSE := docker compose -f deploy/docker-compose.yml --project-directory .

# Where the API gateway listens (same for `make up` and `make dev`) and which admin token the
# helpers send. Without ADMIN_TOKEN in .env they use "demo-token", which `make demo` and
# `make dev` configure; `make up` leaves admin endpoints closed unless .env sets a token.
GATEWAY_PORT ?= 8088
GATEWAY      ?= http://localhost:$(GATEWAY_PORT)
API          := $(GATEWAY)/api/v1
ADMIN        := $(if $(ADMIN_TOKEN),$(ADMIN_TOKEN),demo-token)
AUTH         := -H "Authorization: Bearer $(ADMIN)"
JSON         := $(shell command -v jq >/dev/null 2>&1 && echo "jq ." || echo "python3 -m json.tool")
ID           ?= 1

.DEFAULT_GOAL := help
.PHONY: help doctor \
        up demo down restart clean build logs ps \
        dev dev-stop dev-restart dev-status dev-logs dev-clean \
        status cities climate alerts alerts-all notifications refresh scenario \
        test test-go test-py test-ts lint fmt smoke \
        train vectors

help: ## show this list
	@awk 'BEGIN{FS=":.*## "} /^##@/{printf "\n%s\n", substr($$0,5)} /^[a-zA-Z_-]+:.*## /{printf "  make %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

##@ Run everything in Docker (needs Docker)

up: ## build and start the system with LIVE weather (admin endpoints stay closed unless .env sets ADMIN_TOKEN)
	$(COMPOSE) up -d --build --wait
	@echo; echo "frontend  http://localhost:$${FRONTEND_PORT:-3000}"; echo "api       $(API)/status"

demo: ## build and start with SIMULATED weather + admin token, so you can create a heatwave: make scenario S=extreme
	WEATHER_SOURCE=simulated ADMIN_TOKEN=$(ADMIN) RESOLVE_AFTER=$${RESOLVE_AFTER:-30s} COOLDOWN=$${COOLDOWN:-2m} \
	  $(COMPOSE) up -d --build --wait
	@echo; echo "frontend  http://localhost:$${FRONTEND_PORT:-3000}"; echo "api       $(API)/status"; echo "admin token: $(ADMIN)"
	@echo "try: make scenario S=extreme   |   make alerts   |   make climate ID=1"

down: ## stop the system (data volumes are kept)
	$(COMPOSE) down

restart: ## restart the running containers (optional: SVC=risk)
	$(COMPOSE) restart $(SVC)

clean: ## stop the system and DELETE its data volumes
	$(COMPOSE) down --volumes

build: ## build all images without starting anything
	$(COMPOSE) build

logs: ## follow logs (all services, or one: make logs SVC=risk)
	$(COMPOSE) logs -f --tail=100 $(SVC)

ps: ## show container status and health
	$(COMPOSE) ps

##@ Run everything locally without Docker (needs go, uv, bun)

dev: ## build and start all 7 processes locally with simulated weather
	scripts/dev.sh start

dev-stop: ## stop the local processes (data kept)
	scripts/dev.sh stop

dev-restart: ## stop and start again
	scripts/dev.sh restart

dev-status: ## show which local processes are running
	scripts/dev.sh status

dev-logs: ## follow local logs (all, or one: make dev-logs SVC=risk)
	scripts/dev.sh logs $(SVC)

dev-clean: dev-stop ## stop and delete the local databases, logs and binaries (.dev/)
	rm -rf .dev

##@ Poke the running system (works with `make up`, `make demo` and `make dev`)

status: ## health of every service, with circuit breaker state
	@curl -sS $(API)/status | $(JSON)

cities: ## the watched locations
	@curl -sS $(API)/locations | $(JSON)

climate: ## everything about one location: make climate ID=1
	@curl -sS "$(API)/locations/$(ID)/climate?hourly=false" | $(JSON)

alerts: ## open alerts
	@curl -sS "$(API)/alerts?status=open" | $(JSON)

alerts-all: ## open and resolved alerts
	@curl -sS "$(API)/alerts" | $(JSON)

notifications: ## notification delivery state (admin): make notifications STATUS=failed
	@curl -sS $(AUTH) "$(API)/notifications$(if $(STATUS),?status=$(STATUS))" | $(JSON)

refresh: ## fetch fresh weather for one location now (admin): make refresh ID=1
	@curl -sS -X POST $(AUTH) $(API)/locations/$(ID)/refresh | $(JSON)

scenario: ## set simulated weather for every city (admin): make scenario S=normal|building|extreme
	@test -n "$(S)" || { echo "usage: make scenario S=normal|building|extreme"; exit 2; }
	@curl -sS -X PUT $(AUTH) $(API)/simulation -d '{"scenario":"$(S)"}' | $(JSON)

##@ Test and check code

test: test-go test-py test-ts ## run all unit tests (Go, Python, TypeScript)

test-go: ## Go: vet + tests with the race detector
	cd backend && go vet ./... && go test -race ./...

test-py: ## Python (prediction service) tests
	cd backend/prediction && uv sync --quiet && PYTHONPATH=src uv run pytest -q

test-ts: ## frontend: tests + lint + type check
	cd frontend && bun test && bun run lint && bunx tsc --noEmit

lint: ## frontend lint only
	cd frontend && bun run lint

fmt: ## format the Go code
	cd backend && gofmt -w .

smoke: ## end-to-end test in Docker: build, create a heatwave, stop a service, recover (~5 min first time)
	scripts/smoke.sh

##@ Tools

train: ## retrain the heatwave model (downloads weather history; slow; rewrites model.json)
	cd scripts/train && uv run python train.py

vectors: ## regenerate the shared engine test vectors from the TypeScript engine
	cd frontend && bun run vectors

doctor: ## check which tools are installed
	@for t in docker go uv bun node python3 make curl; do \
	  if command -v $$t >/dev/null 2>&1; then \
	    v=$$( ($$t $$([ $$t = go ] && echo version || echo --version) 2>&1 | head -1) | cut -c1-70 ); printf '  ✓ %-8s %s\n' $$t "$$v"; \
	  else printf '  ✗ %-8s not installed\n' $$t; fi; done
	@docker compose version >/dev/null 2>&1 && echo "  ✓ docker compose plugin" || echo "  ✗ docker compose plugin missing"
	@docker info >/dev/null 2>&1 && echo "  ✓ docker daemon reachable" || echo "  ✗ docker daemon not reachable (start Docker Desktop)"
