SHELL := /bin/bash
COMPOSE := docker compose -f deploy/docker-compose.yml
DATABASE_URL ?= postgres://ledger:ledger@localhost:5432/ledger?sslmode=disable
ADMIN_API_KEY ?= dev-admin-key-change-me

.PHONY: help build run test test-short lint fmt up down logs psql clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build api and worker binaries into ./bin
	go build -o bin/ ./cmd/...

run: ## Run the API locally against DATABASE_URL
	DATABASE_URL=$(DATABASE_URL) ADMIN_API_KEY=$(ADMIN_API_KEY) go run ./cmd/api

test: ## Run all tests (needs Docker for Postgres testcontainers)
	go test -race -count=1 ./...

test-short: ## Run unit tests only
	go test -short -race -count=1 ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format code
	gofmt -s -w .

up: ## Start the full stack with docker compose
	$(COMPOSE) up -d --build

down: ## Stop the stack
	$(COMPOSE) down

logs: ## Tail stack logs
	$(COMPOSE) logs -f

psql: ## Open psql against the compose database
	psql "$(DATABASE_URL)"

clean: ## Remove build output
	rm -rf bin
