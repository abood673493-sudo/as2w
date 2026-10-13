# SSH Manager Panel - common developer and deployment tasks.
# Targets target GNU Make on Ubuntu Server LTS (see docs/ubuntu-install.md).

SHELL := /bin/bash

BACKEND_DIR  := backend
FRONTEND_DIR := frontend

GO  ?= go
NPM ?= npm

# Override on the command line, e.g.
#   make migrate-up DATABASE_URL=postgres://sshmp:secret@127.0.0.1:5432/sshmp
DATABASE_URL ?= postgres://sshmp:sshmp@127.0.0.1:5432/sshmp?sslmode=disable

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_.-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-28s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Backend (Go)
# ---------------------------------------------------------------------------
.PHONY: backend-deps
backend-deps: ## Download Go module dependencies
	cd $(BACKEND_DIR) && $(GO) mod download

.PHONY: backend-build
backend-build: ## Compile the API server binary
	cd $(BACKEND_DIR) && $(GO) build -trimpath -o bin/ssh-manager-panel-server ./cmd/server

.PHONY: backend-run
backend-run: ## Run the API server locally
	cd $(BACKEND_DIR) && $(GO) run ./cmd/server serve

.PHONY: backend-test
backend-test: ## Run backend unit tests
	cd $(BACKEND_DIR) && $(GO) test ./... -count=1

.PHONY: backend-test-integration
backend-test-integration: ## Run backend tests including PostgreSQL integration tests
	cd $(BACKEND_DIR) && SSHM_TEST_DATABASE_URL="$(DATABASE_URL)" $(GO) test ./... -count=1

.PHONY: backend-vet
backend-vet: ## Run go vet
	cd $(BACKEND_DIR) && $(GO) vet ./...

# ---------------------------------------------------------------------------
# Database migrations (implemented in Go, embedded SQL files)
# ---------------------------------------------------------------------------
.PHONY: migrate-up
migrate-up: ## Apply all pending migrations
	cd $(BACKEND_DIR) && DATABASE_URL="$(DATABASE_URL)" $(GO) run ./cmd/server --migrate=up

.PHONY: migrate-down
migrate-down: ## Roll back the most recently applied migration
	cd $(BACKEND_DIR) && DATABASE_URL="$(DATABASE_URL)" $(GO) run ./cmd/server --migrate=down

.PHONY: migrate-status
migrate-status: ## Show migration status
	cd $(BACKEND_DIR) && DATABASE_URL="$(DATABASE_URL)" $(GO) run ./cmd/server --migrate=status

.PHONY: bootstrap-admin
bootstrap-admin: ## Create the first administrator from BOOTSTRAP_ADMIN_* env vars
	cd $(BACKEND_DIR) && $(GO) run ./cmd/server bootstrap-admin

# ---------------------------------------------------------------------------
# Frontend (React / Vite)
# ---------------------------------------------------------------------------
.PHONY: frontend-install
frontend-install: ## Install frontend dependencies
	cd $(FRONTEND_DIR) && $(NPM) ci

.PHONY: frontend-dev
frontend-dev: ## Start the Vite dev server
	cd $(FRONTEND_DIR) && $(NPM) run dev

.PHONY: frontend-build
frontend-build: ## Type-check and build the frontend
	cd $(FRONTEND_DIR) && $(NPM) run build

.PHONY: frontend-test
frontend-test: ## Run frontend unit tests
	cd $(FRONTEND_DIR) && $(NPM) run test -- --run

# ---------------------------------------------------------------------------
# Aggregates
# ---------------------------------------------------------------------------
.PHONY: build
build: backend-build frontend-build ## Build backend and frontend

.PHONY: test
test: backend-test frontend-test ## Run all fast test suites

.PHONY: lint
lint: backend-vet ## Run static analysis

# ---------------------------------------------------------------------------
# Docker Compose
# ---------------------------------------------------------------------------
.PHONY: docker-up
docker-up: ## Start the Docker Compose stack
	docker compose -f deploy/docker-compose.yml up -d --build

.PHONY: docker-down
docker-down: ## Stop the Docker Compose stack
	docker compose -f deploy/docker-compose.yml down

.PHONY: docker-logs
docker-logs: ## Follow Docker Compose logs
	docker compose -f deploy/docker-compose.yml logs -f --tail=200