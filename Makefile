SHELL := /bin/sh

GO ?= go
GOFLAGS ?=
PKGS ?= ./...

CMD ?= ./cmd/sweagent
BINARY ?= sweagent
BIN_DIR ?= bin

CONFIG ?= configs/default.yaml
REPO ?= .
TASK ?= finish immediately
ARGS ?=

.PHONY: help fmt vet test test-race tidy check build run run-json tools config clean

help: ## Show available make targets.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go packages.
	$(GO) fmt $(PKGS)

vet: ## Run go vet.
	$(GO) vet $(PKGS)

test: ## Run unit tests.
	$(GO) test $(GOFLAGS) $(PKGS)

test-race: ## Run unit tests with the race detector.
	$(GO) test -race $(GOFLAGS) $(PKGS)

tidy: ## Update go.mod and go.sum.
	$(GO) mod tidy

check: fmt vet test ## Run formatting, vet, and tests.

build: ## Build the sweagent binary into BIN_DIR.
	mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY) $(CMD)

run: ## Run a mock SWE-agent task. Override TASK, REPO, CONFIG, and ARGS as needed.
	$(GO) run $(CMD) run --config $(CONFIG) --task "$(TASK)" --repo $(REPO) $(ARGS)

run-json: ## Run a mock SWE-agent task and print JSON.
	$(GO) run $(CMD) run --config $(CONFIG) --task "$(TASK)" --repo $(REPO) --json $(ARGS)

tools: ## List enabled tools.
	$(GO) run $(CMD) tools --config $(CONFIG)

config: ## Print merged configuration.
	$(GO) run $(CMD) config --config $(CONFIG)

clean: ## Remove build outputs.
	rm -rf $(BIN_DIR)
