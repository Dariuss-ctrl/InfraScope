# InfraScope developer commands.
# Every target is a thin wrapper; docs/runbook.md lists the plain docker compose
# equivalents for machines without make.

COMPOSE       := docker compose --env-file .env -f deployments/docker-compose.yml
COMPOSE_DEBUG := $(COMPOSE) -f deployments/docker-compose.debug.yml
GO_MODULES    := apps/api apps/demo-service
DASHBOARD     := apps/dashboard

RATE     ?= 20
DURATION ?= 2m
S        ?=

.DEFAULT_GOAL := help
.PHONY: help up up-debug down reset ps logs smoke load config-check lint test test-integration fmt

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-18s %s\n", $$1, $$2}'

.env:
	cp .env.example .env
	@echo "Created .env from .env.example. Change its passwords before exposing anything."

up: .env ## Build and start the stack, waiting until every service is healthy
	$(COMPOSE) up -d --build --wait

up-debug: .env ## Same as up, plus internal ports published on localhost
	$(COMPOSE_DEBUG) up -d --build --wait

down: ## Stop the stack and keep its data
	$(COMPOSE) down

reset: ## Stop the stack and delete all data volumes
	$(COMPOSE) down -v --remove-orphans

ps: ## Show container status and health
	$(COMPOSE) ps

logs: ## Follow logs for all services, or one with S=<service>
	$(COMPOSE) logs -f --tail=200 $(S)

smoke: ## Check a running stack end to end
	bash scripts/smoke.sh

load: .env ## Run the k6 workload (RATE=20 DURATION=2m)
	$(COMPOSE) --profile load run --rm -e RATE=$(RATE) -e DURATION=$(DURATION) k6

config-check: .env ## Validate the Compose configuration
	$(COMPOSE) config --quiet

lint: ## Static checks for Go and the dashboard
	@for m in $(GO_MODULES); do \
		echo "== $$m"; \
		unformatted=$$(gofmt -l $$m); \
		if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi; \
		(cd $$m && go vet ./...) || exit 1; \
	done
	cd $(DASHBOARD) && npm run lint && npm run typecheck

test: ## Unit tests for Go and the dashboard
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done
	cd $(DASHBOARD) && npm test

test-integration: ## Integration tests (testcontainers-go, from Phase 2)
	@echo "Integration tests arrive in Phase 2; see tests/integration/README.md"

fmt: ## Format Go code
	gofmt -w $(GO_MODULES)
