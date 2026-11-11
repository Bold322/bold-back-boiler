.PHONY: help build run test test-race test-integration test-one cover vet fmt tidy sqlc docker-build db-up db-down clean seed superadmin

# Auto-load .env into every recipe's environment. The leading dash makes a
# missing file a no-op (fine in CI), and the bareword `export` pushes every
# Make variable (including the ones .env defines) into each recipe.
# Caveats: values are parsed by Make, so unquoted '#' starts a comment and
# unescaped '$' is interpreted as a variable expansion ($$ for a literal $).
-include .env
export

BIN_DIR  := bin
BINARY   := $(BIN_DIR)/server
MAIN_PKG := ./cmd/server
IMAGE    := bold-back-boiler
TAG      := latest
PKG      ?= ./...
NAME     ?= .

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile cmd/server into $(BINARY)
	mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags="-s -w" -o $(BINARY) $(MAIN_PKG)

run: build ## Build and run the server (auto-loads .env if present)
	./$(BINARY)

test: ## Unit tests only (Postgres tests skip)
	go test $(PKG)

test-race: ## Unit tests with the race detector
	go test -race $(PKG)

test-integration: ## All tests including Postgres (RUN_DB_TESTS=1; TEST_DATABASE_URL overrides the compose default)
	RUN_DB_TESTS=1 go test ./migrations
	RUN_DB_TESTS=1 go test -p=1 $(PKG)

test-one: ## Run one test: make test-one NAME=TestFoo PKG=./internal/auth
	go test -run $(NAME) $(PKG)

cover: ## Generate coverage.txt and coverage.html
	go test -coverprofile=coverage.txt ./...
	go tool cover -html=coverage.txt -o coverage.html

vet: ## Run go vet on the whole module
	go vet ./...

fmt: ## Apply gofmt -w to the tree
	gofmt -w .

tidy: ## Run go mod tidy
	go mod tidy

sqlc: ## Regenerate internal/db/sqlc from internal/db/queries
	sqlc generate

docker-build: ## Build the Docker image $(IMAGE):$(TAG)
	docker build -t $(IMAGE):$(TAG) .

db-up: ## Start the local Postgres (docker compose)
	docker compose up -d postgres

db-down: ## Stop the local Postgres and drop its volume
	docker compose down -v

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.txt coverage.html

seed: ## Create a tenant + initial admin invite: make seed NAME=Demo SLUG=demo EMAIL=admin@demo.com
	go run ./cmd/seed -name="$(NAME)" -slug="$(SLUG)" -admin-email="$(EMAIL)"

superadmin: ## Create a superadmin: make superadmin EMAIL=... NAME=... PASS=...
	go run ./cmd/superadmin-create -email="$(EMAIL)" -display-name="$(NAME)" -password="$(PASS)"
