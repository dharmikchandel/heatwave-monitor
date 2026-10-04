# Developer shortcuts. `make help` lists them.

COMPOSE := docker compose -f deploy/docker-compose.yml --project-directory .

.DEFAULT_GOAL := help
.PHONY: help build up down clean logs ps smoke test test-go test-py test-ts

help: ## show this list
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-9s %s\n", $$1, $$2}'

build: ## build every image
	$(COMPOSE) build

up: ## build and start the whole system in the background (settings from .env)
	$(COMPOSE) up -d --build --wait
	@echo "frontend: http://localhost:$${FRONTEND_PORT:-3000}   api: http://localhost:$${GATEWAY_PORT:-8088}/api/v1/status"

down: ## stop the system (keeps the data volumes)
	$(COMPOSE) down

clean: ## stop the system and delete its data volumes
	$(COMPOSE) down --volumes

logs: ## follow the logs of every service
	$(COMPOSE) logs -f --tail=100

ps: ## show service status and health
	$(COMPOSE) ps

smoke: ## end-to-end test: build, run, create a heatwave, stop a service, recover (needs only Docker)
	scripts/smoke.sh

test: test-go test-py test-ts ## all unit tests

test-go:
	cd services && go vet ./... && go test -race ./...

test-py:
	cd services/prediction && PYTHONPATH=src uv run pytest -q

test-ts:
	bun test && bun run lint && bunx tsc --noEmit
