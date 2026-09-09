.PHONY: all build build-all build-agent build-server build-ctl build-ctl-all test test-e2e clean docker-build help

VERSION ?= 1.1.0
BIN_DIR ?= bin
LDFLAGS = -s -w -X main.version=$(VERSION)

all: build

build: build-server build-ctl build-agent

build-all:
	@echo "==> Building all multi-platform binaries (Android, Linux, macOS, Windows)..."
	./scripts/build_all.sh

build-server:
	@echo "==> Building adblink-server..."
	mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-server ./cmd/adblink-server

build-ctl:
	@echo "==> Building adblink-ctl..."
	mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/adblink-ctl ./cmd/adblink-ctl
build-ctl-all:
	@echo "==> Building adblink-ctl for all PC platforms (Linux, macOS, Windows)..."
	mkdir -p $(BIN_DIR)/linux $(BIN_DIR)/darwin $(BIN_DIR)/windows
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/linux/adblink-ctl-linux-amd64 ./cmd/adblink-ctl
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/linux/adblink-ctl-linux-arm64 ./cmd/adblink-ctl
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/darwin/adblink-ctl-darwin-arm64 ./cmd/adblink-ctl
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/darwin/adblink-ctl-darwin-amd64 ./cmd/adblink-ctl
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/windows/adblink-ctl-windows-amd64.exe ./cmd/adblink-ctl
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/windows/adblink-ctl-windows-arm64.exe ./cmd/adblink-ctl
	@echo "==> Multi-platform adblink-ctl binaries built in $(BIN_DIR)/"

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
	@echo "  build-ctl     - Build adblink-ctl CLI for current host"
	@echo "  build-ctl-all - Cross-compile adblink-ctl for Linux, macOS, and Windows"
	@echo "  test          - Run all Go package tests with race detector"
	@echo "  test-e2e      - Run end-to-end integration test"
	@echo "  docker-build  - Build Docker container for adblink-server"
	@echo "  clean         - Remove compiled binaries"
