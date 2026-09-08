#!/usr/bin/env bash
set -euo pipefail

# scripts/deploy.sh: One-click deploy script for AdbLink Agent onto an Android device

SERVER_ADDR="${1:-}"
ADB_TARGET="${2:-}"

if [[ -z "$SERVER_ADDR" ]]; then
    echo "Usage: $0 <server_ip:port> [adb_device_serial] [token]"
    echo "Example: $0 192.168.1.100:9000"
    echo "Example: $0 192.168.1.100:9000 127.0.0.1:5558 secret123"
    exit 1
fi

TOKEN="${3:-}"

ADB_CMD="adb"
if [[ -n "$ADB_TARGET" ]]; then
    ADB_CMD="adb -s $ADB_TARGET"
fi

echo "==> Checking ADB connection..."
$ADB_CMD get-state >/dev/null 2>&1 || {
    echo "Error: ADB device not found or offline. Check 'adb devices'."
    exit 1
}

echo "==> Detecting Android device architecture..."
ABI=$($ADB_CMD shell getprop ro.product.cpu.abi | tr -d '\r')
MODEL=$($ADB_CMD shell getprop ro.product.model | tr -d '\r')
echo "Detected Device: $MODEL ($ABI)"

# Select appropriate binary
AGENT_BIN=""
case "$ABI" in
    arm64*|aarch64*)
        AGENT_BIN="bin/adblink-agent-android-arm64"
        ;;
    armeabi*|armv7*)
        AGENT_BIN="bin/adblink-agent-android-arm"
        ;;
    x86_64*)
        AGENT_BIN="bin/adblink-agent-android-amd64"
        ;;
    x86*)
        AGENT_BIN="bin/adblink-agent-android-386"
        ;;
    *)
        echo "Unknown architecture $ABI, falling back to arm64..."
        AGENT_BIN="bin/adblink-agent-android-arm64"
        ;;
esac

# Build if binary doesn't exist
if [[ ! -f "$AGENT_BIN" ]]; then
    echo "==> Binary $AGENT_BIN not found, building..."
    make build-agent
fi

REMOTE_DIR="/data/local/tmp"
REMOTE_BIN="$REMOTE_DIR/adblink-agent"
REMOTE_LOG="$REMOTE_DIR/adblink.log"

echo "==> Stopping any previous agent on device..."
$ADB_CMD shell "pkill -9 adblink-agent 2>/dev/null || true"

echo "==> Pushing $AGENT_BIN to device $REMOTE_BIN..."
$ADB_CMD push "$AGENT_BIN" "$REMOTE_BIN"
$ADB_CMD shell chmod +x "$REMOTE_BIN"

echo "==> Starting adblink-agent in background..."
EXTRA_ARGS=""
if [[ -n "$TOKEN" ]]; then
    EXTRA_ARGS="-token $TOKEN"
fi

$ADB_CMD shell "nohup $REMOTE_BIN -server $SERVER_ADDR $EXTRA_ARGS > $REMOTE_LOG 2>&1 &"

sleep 1

echo "==> Agent output log:"
$ADB_CMD shell "cat $REMOTE_LOG"

echo ""
echo "==> Deployment complete! The agent is running in the background."
echo "View logs anytime with: $ADB_CMD shell cat $REMOTE_LOG"
