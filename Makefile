# InfraScope developer commands.
# Every target is a thin wrapper; docs/runbook.md lists the plain docker compose
# equivalents for machines without make.

COMPOSE       := docker compose --env-file .env -f deployments/docker-compose.yml
COMPOSE_DEBUG := $(COMPOSE) -f deployments/docker-compose.debug.yml
GO_MODULES    := apps/api apps/demo-service tests/pipeline
DASHBOARD     := apps/dashboard
SHOP_PSQL     := $(COMPOSE) exec -T postgres psql -U postgres -d shop -v ON_ERROR_STOP=1 -c
# Go tooling runs in pinned containers, so no local install is needed.
IN_GO_IMAGE   = docker run --rm -v "$(CURDIR):/repo" -w /repo/$(1)

RATE     ?= 20
DURATION ?= 2m
S        ?=

.DEFAULT_GOAL := help
.PHONY: help up up-debug down reset ps logs smoke load demo-fix demo-reset config-check lint vulncheck test test-integration test-pipeline fmt

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

load: .env ## Run the k6 workload against the running stack (RATE=20 DURATION=2m)
	@# --no-deps: never (re)start the system under test from the load generator.
	$(COMPOSE) --profile load run --rm --no-deps -e RATE=$(RATE) -e DURATION=$(DURATION) k6

demo-fix: ## Scenario S1: add the missing index on orders.customer_email
	$(SHOP_PSQL) "CREATE INDEX CONCURRENTLY IF NOT EXISTS orders_customer_email_idx ON orders (customer_email)"

demo-reset: ## Scenario S1: drop the index again (the "bad deploy")
	$(SHOP_PSQL) "DROP INDEX CONCURRENTLY IF EXISTS orders_customer_email_idx"

config-check: .env ## Validate the Compose configuration
	$(COMPOSE) config --quiet

lint: ## Static checks for Go and the dashboard
	@for m in $(GO_MODULES); do \
		echo "== $$m"; \
		unformatted=$$(gofmt -l $$m); \
		if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi; \
		(cd $$m && go vet ./...) || exit 1; \
		$(call IN_GO_IMAGE,$$m) golangci/golangci-lint:v2.15.0 golangci-lint run ./... || exit 1; \
	done
	cd $(DASHBOARD) && npm run lint && npm run typecheck

vulncheck: ## Known-vulnerability scan of every Go module (govulncheck)
	@for m in $(GO_MODULES); do \
		echo "== $$m"; \
		$(call IN_GO_IMAGE,$$m) golang:1.27.3-alpine go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./... || exit 1; \
	done

test: ## Unit tests for Go and the dashboard
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done
	cd $(DASHBOARD) && npm test

test-integration: ## Integration tests (testcontainers-go, from Phase 2)
	@echo "Integration tests arrive in Phase 2; see tests/integration/README.md"

test-pipeline: .env ## End-to-end telemetry test; needs a stack started with make up-debug
	set -a && . ./.env && set +a && cd tests/pipeline && INFRASCOPE_PIPELINE=1 go test -count=1 -v ./...

fmt: ## Format Go code
	gofmt -w $(GO_MODULES)
