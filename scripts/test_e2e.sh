#!/usr/bin/env bash
set -euo pipefail

# scripts/test_e2e.sh: End-to-end integration test for AdbLink

SERVER_PORT=9290
WEB_PORT=9291
ADB_EXPOSED_PORT=55570
SERVER_PID=""

cleanup() {
    echo "==> Cleaning up test environment..."
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
    fi
    adb disconnect "127.0.0.1:$ADB_EXPOSED_PORT" 2>/dev/null || true
    adb -s 127.0.0.1:5558 shell "pkill -9 adblink-agent" 2>/dev/null || true
    rm -f /tmp/e2e_test_in.bin /tmp/e2e_test_out.bin
}
trap cleanup EXIT

echo "==> 1. Building binaries..."
make build

echo "==> 2. Checking local test Android device (127.0.0.1:5558)..."
if ! adb -s 127.0.0.1:5558 get-state >/dev/null 2>&1; then
    echo "Android test instance 127.0.0.1:5558 not available, skipping live hardware e2e test."
    exit 0
fi

echo "==> 3. Starting AdbLink Server on port $SERVER_PORT..."
./bin/adblink-server -listen "0.0.0.0:$SERVER_PORT" -web "0.0.0.0:$WEB_PORT" -host "127.0.0.1" -port-min "$ADB_EXPOSED_PORT" -port-max "$ADB_EXPOSED_PORT" > /tmp/adblink_server_e2e.log 2>&1 &
SERVER_PID=$!
sleep 1

# Verify server health
curl -sf "http://127.0.0.1:$WEB_PORT/api/v1/health" >/dev/null || {
    echo "Error: Server failed to start. Logs:"
    cat /tmp/adblink_server_e2e.log
    exit 1
}

echo "==> 4. Deploying Agent to Android device..."
./scripts/deploy.sh "172.17.0.1:$SERVER_PORT" "127.0.0.1:5558"
sleep 2

echo "==> 5. Verifying device in Web API..."
DEVICE_COUNT=$(curl -sf "http://127.0.0.1:$WEB_PORT/api/v1/devices" | grep -o '"status":"ONLINE"' | wc -l)
if [[ "$DEVICE_COUNT" -lt 1 ]]; then
    echo "Error: Device failed to register online. Server logs:"
    cat /tmp/adblink_server_e2e.log
    exit 1
fi
echo "Device registered and ONLINE!"

echo "==> 6. Testing adb connect to exposed port 127.0.0.1:$ADB_EXPOSED_PORT..."
adb connect "127.0.0.1:$ADB_EXPOSED_PORT"
sleep 1

echo "==> 7. Executing remote shell command via reverse tunnel..."
REMOTE_MODEL=$(adb -s "127.0.0.1:$ADB_EXPOSED_PORT" shell getprop ro.product.model | tr -d '\r')
echo "Remote device model via reverse tunnel: $REMOTE_MODEL"
if [[ -z "$REMOTE_MODEL" ]]; then
    echo "Error: Failed to retrieve remote model via reverse tunnel"
    exit 1
fi

echo "==> 8. Testing 2MB file transfer integrity..."
dd if=/dev/urandom of=/tmp/e2e_test_in.bin bs=1M count=2 2>/dev/null
IN_HASH=$(sha256sum /tmp/e2e_test_in.bin | awk '{print $1}')

adb -s "127.0.0.1:$ADB_EXPOSED_PORT" push /tmp/e2e_test_in.bin /data/local/tmp/e2e_test.bin
adb -s "127.0.0.1:$ADB_EXPOSED_PORT" pull /data/local/tmp/e2e_test.bin /tmp/e2e_test_out.bin
adb -s "127.0.0.1:$ADB_EXPOSED_PORT" shell rm -f /data/local/tmp/e2e_test.bin

OUT_HASH=$(sha256sum /tmp/e2e_test_out.bin | awk '{print $1}')

if [[ "$IN_HASH" != "$OUT_HASH" ]]; then
    echo "Error: Hash mismatch! In: $IN_HASH, Out: $OUT_HASH"
    exit 1
fi
echo "SHA-256 Checksum Verified: $IN_HASH"

echo "=========================================="
echo "  ALL E2E INTEGRATION TESTS PASSED!       "
echo "=========================================="
