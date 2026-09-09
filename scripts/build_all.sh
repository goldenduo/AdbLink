#!/usr/bin/env bash
set -euo pipefail

# scripts/build_all.sh: Cross-compile AdbLink for Android (arm64, x64), Linux, macOS, and Windows

VERSION="${VERSION:-1.1.1}"
LDFLAGS="-s -w -X main.version=${VERSION}"

BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${BASE_DIR}/bin"

echo "==> Preparing output directories in ${BIN_DIR}..."
mkdir -p "${BIN_DIR}/android"
mkdir -p "${BIN_DIR}/linux"
mkdir -p "${BIN_DIR}/darwin"
mkdir -p "${BIN_DIR}/windows"

echo "========================================================="
echo " 1. Compiling Android Native Agent (ARM64 & x86_64/x64)"
echo "========================================================="

echo "  -> Android ARM64 (aarch64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/android/adblink-agent-arm64" ./cmd/adblink-agent

echo "  -> Android x86_64 (x64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/android/adblink-agent-x86_64" ./cmd/adblink-agent

echo "  -> Android ARMv7 (32-bit)..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/android/adblink-agent-armv7" ./cmd/adblink-agent

echo "  -> Android x86 (32-bit)..."
CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/android/adblink-agent-x86" ./cmd/adblink-agent

echo "========================================================="
echo " 2. Compiling PC - Linux (x86_64 & ARM64)"
echo "========================================================="

echo "  -> Linux amd64 (x64) server & ctl..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/linux/adblink-server-linux-amd64" ./cmd/adblink-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/linux/adblink-ctl-linux-amd64" ./cmd/adblink-ctl

echo "  -> Linux arm64 server & ctl..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/linux/adblink-server-linux-arm64" ./cmd/adblink-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/linux/adblink-ctl-linux-arm64" ./cmd/adblink-ctl

echo "========================================================="
echo " 3. Compiling PC - macOS (Apple Silicon & Intel)"
echo "========================================================="

echo "  -> macOS arm64 (Apple Silicon M1/M2/M3/M4) server & ctl..."
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/darwin/adblink-server-darwin-arm64" ./cmd/adblink-server
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/darwin/adblink-ctl-darwin-arm64" ./cmd/adblink-ctl

echo "  -> macOS amd64 (Intel) server & ctl..."
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/darwin/adblink-server-darwin-amd64" ./cmd/adblink-server
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/darwin/adblink-ctl-darwin-amd64" ./cmd/adblink-ctl

echo "========================================================="
echo " 4. Compiling PC - Windows (x86_64 & ARM64)"
echo "========================================================="

echo "  -> Windows amd64 (x64) server & ctl..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/windows/adblink-server-windows-amd64.exe" ./cmd/adblink-server
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/windows/adblink-ctl-windows-amd64.exe" ./cmd/adblink-ctl

echo "  -> Windows arm64 server & ctl..."
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/windows/adblink-server-windows-arm64.exe" ./cmd/adblink-server
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/windows/adblink-ctl-windows-arm64.exe" ./cmd/adblink-ctl

# Also maintain root bin links for convenience and backwards compatibility
HOST_OS="$(go env GOOS)"
HOST_ARCH="$(go env GOARCH)"
if [[ -f "${BIN_DIR}/${HOST_OS}/adblink-server-${HOST_OS}-${HOST_ARCH}" ]]; then
    cp "${BIN_DIR}/${HOST_OS}/adblink-server-${HOST_OS}-${HOST_ARCH}" "${BIN_DIR}/adblink-server"
    cp "${BIN_DIR}/${HOST_OS}/adblink-ctl-${HOST_OS}-${HOST_ARCH}" "${BIN_DIR}/adblink-ctl"
fi
cp "${BIN_DIR}/android/adblink-agent-arm64" "${BIN_DIR}/adblink-agent-android-arm64"
cp "${BIN_DIR}/android/adblink-agent-x86_64" "${BIN_DIR}/adblink-agent-android-amd64"
cp "${BIN_DIR}/android/adblink-agent-armv7" "${BIN_DIR}/adblink-agent-android-arm"
cp "${BIN_DIR}/android/adblink-agent-x86" "${BIN_DIR}/adblink-agent-android-386"

echo "========================================================="
echo " ALL MULTI-PLATFORM BINARIES SUCCESSFULLY COMPILED!"
echo "========================================================="
find "${BIN_DIR}" -type f -exec ls -lh {} +
