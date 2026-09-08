.PHONY: all build build-agent build-server build-ctl test test-e2e clean docker-build help

VERSION ?= 1.0.0
BIN_DIR ?= bin
LDFLAGS = -s -w -X main.version=$(VERSION)

all: build

build: build-server build-ctl build-agent

build-server:
	@echo "==> Building adblink-server..."
	mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-server ./cmd/adblink-server

build-ctl:
	@echo "==> Building adblink-ctl..."
	mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-ctl ./cmd/adblink-ctl

build-agent:
	@echo "==> Building adblink-agent for Android architectures..."
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-agent-android-arm64 ./cmd/adblink-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-agent-android-arm ./cmd/adblink-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-agent-android-amd64 ./cmd/adblink-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-agent-android-386 ./cmd/adblink-agent
	@echo "==> All agent binaries built in $(BIN_DIR)/"

test:
	@echo "==> Running unit & integration tests..."
	go test -v -race ./pkg/...

test-e2e: build
	@echo "==> Running end-to-end integration tests..."
	./scripts/test_e2e.sh

clean:
	@echo "==> Cleaning build artifacts..."
	rm -rf $(BIN_DIR)

docker-build:
	@echo "==> Building Docker image for adblink-server..."
	docker build -t adblink-server:$(VERSION) -t adblink-server:latest .

help:
	@echo "AdbLink Makefile targets:"
	@echo "  build         - Build server, ctl, and all multi-arch agent binaries"
	@echo "  build-server  - Build adblink-server"
	@echo "  build-ctl     - Build adblink-ctl CLI"
	@echo "  build-agent   - Cross-compile static agent for arm64, arm, amd64, 386"
	@echo "  test          - Run all Go package tests with race detector"
	@echo "  test-e2e      - Run end-to-end integration test"
	@echo "  docker-build  - Build Docker container for adblink-server"
	@echo "  clean         - Remove compiled binaries"
