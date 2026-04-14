SHELL := /bin/sh

.DEFAULT_GOAL := help

APP_NAME ?= weather-risk-api
GO ?= go
PKG ?= ./...
PORT ?= 8080
BIN_DIR ?= bin
BIN ?= $(BIN_DIR)/$(APP_NAME)
IMAGE ?= $(APP_NAME):local
CONTAINER ?= $(APP_NAME)
GOCACHE ?= $(CURDIR)/.cache/go-build
GOTMPDIR ?= $(CURDIR)/.tmp/go
COVERPROFILE ?= coverage.out
TEST_FLAGS ?=
RUN_ARGS ?=

GO_ENV = GOCACHE=$(GOCACHE) GOTMPDIR=$(GOTMPDIR)

.PHONY: help setup run build test test-race cover fmt vet check clean docker-build docker-run docker-run-bg docker-stop docker-logs

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-15s %s\n", $$1, $$2} END {printf "\n"}' $(MAKEFILE_LIST)

setup: ## Create local build and cache directories
	@mkdir -p $(BIN_DIR) $(GOCACHE) $(GOTMPDIR)

run: setup ## Run the API locally on PORT (default: 8080)
	@$(GO_ENV) PORT=$(PORT) $(GO) run . $(RUN_ARGS)

build: setup ## Build the binary into bin/
	@$(GO_ENV) $(GO) build -o $(BIN) .
	@printf "Built %s\n" "$(BIN)"

test: setup ## Run unit tests
	@$(GO_ENV) $(GO) test $(TEST_FLAGS) $(PKG)

test-race: setup ## Run tests with the race detector
	@$(GO_ENV) $(GO) test -race $(TEST_FLAGS) $(PKG)

cover: setup ## Run tests with coverage output
	@$(GO_ENV) $(GO) test -coverprofile=$(COVERPROFILE) $(TEST_FLAGS) $(PKG)
	@printf "Wrote %s\n" "$(COVERPROFILE)"

fmt: ## Format Go sources
	@$(GO) fmt $(PKG)

vet: setup ## Run go vet
	@$(GO_ENV) $(GO) vet $(PKG)

check: fmt vet test build ## Run the common local quality gate

clean: ## Remove local build artifacts and caches
	@rm -rf $(BIN_DIR) $(COVERPROFILE) .cache .tmp

docker-build: ## Build the Docker image
	docker build -t $(IMAGE) .

docker-run: docker-build ## Recreate and run the container in the foreground
	-docker rm -f $(CONTAINER)
	docker run --name $(CONTAINER) -p $(PORT):8080 $(IMAGE)

docker-run-bg: docker-build ## Recreate and run the container in the background
	-docker rm -f $(CONTAINER)
	docker run -d --name $(CONTAINER) -p $(PORT):8080 $(IMAGE)

docker-stop: ## Stop and remove the container
	-docker rm -f $(CONTAINER)

docker-logs: ## Tail container logs
	docker logs -f $(CONTAINER)
