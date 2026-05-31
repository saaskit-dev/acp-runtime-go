.PHONY: help clean build lint test test-race runtime simulator harness-admission harness-full install

BIN_DIR ?= bin
RUNTIME_AGENT := $(word 2,$(MAKECMDGOALS))

help:
	@printf '%s\n' \
		'Common targets:' \
		'  make build' \
		'  make lint' \
		'  make test' \
		'  make test-race' \
		'  make runtime simulator' \
		'  make runtime <registry-agent-id>' \
		'  make harness-admission' \
		'  make clean'

clean:
	rm -rf $(BIN_DIR) .tmp harness-outputs acp-runtime acp-simulator-agent acp-harness

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/acp-runtime ./cmd/acp-runtime
	go build -o $(BIN_DIR)/acp-simulator-agent ./cmd/acp-simulator-agent
	go build -o $(BIN_DIR)/acp-harness ./cmd/acp-harness

lint:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

runtime: build
	@if [ -z "$(RUNTIME_AGENT)" ]; then \
		echo 'usage: make runtime <agent-id>'; \
		exit 2; \
	fi
	$(BIN_DIR)/acp-runtime "$(RUNTIME_AGENT)"

simulator:
	@:

harness-admission: build
	$(BIN_DIR)/acp-harness --case harness/cases/05-session-prompt.json --simulator-bin $(BIN_DIR)/acp-simulator-agent

harness-full: build
	@for case_file in harness/cases/*.json; do \
		$(BIN_DIR)/acp-harness --case "$$case_file" --simulator-bin $(BIN_DIR)/acp-simulator-agent || exit $$?; \
	done

install: build
	install -m 0755 $(BIN_DIR)/acp-runtime /usr/local/bin/acp-runtime
	install -m 0755 $(BIN_DIR)/acp-simulator-agent /usr/local/bin/acp-simulator-agent
	install -m 0755 $(BIN_DIR)/acp-harness /usr/local/bin/acp-harness

%:
	@if [ "$@" = "$(firstword $(MAKECMDGOALS))" ]; then \
		echo "Unknown target: $@"; \
		exit 2; \
	fi
