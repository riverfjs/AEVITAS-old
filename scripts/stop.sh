#!/bin/bash
# Stop aevitas gateway with pid fallback detection

set -e

PID_FILE="${HOME}/.aevitas/aevitas.pid"
ts() { date "+%Y-%m-%dT%H:%M:%S%z"; }
echo "[$(ts)] [stop.sh] begin"

TARGET_PID=""
if [ -f "$PID_FILE" ]; then
    TARGET_PID="$(cat "$PID_FILE" 2>/dev/null || true)"
fi
case "$TARGET_PID" in
    ''|*[!0-9]*) TARGET_PID="" ;;
esac
if [ -n "$TARGET_PID" ] && ! kill -0 "$TARGET_PID" 2>/dev/null; then
    TARGET_PID=""
fi
if [ -z "$TARGET_PID" ]; then
    TARGET_PID="$(pgrep -f "\\.aevitas/bin/aevitas gateway" | head -n 1 || true)"
fi
if [ -z "$TARGET_PID" ]; then
    rm -f "$PID_FILE"
    echo "aevitas gateway is not running"
    echo "[$(ts)] [stop.sh] no gateway process found, done"
    exit 0
fi
echo "Stopping aevitas gateway (PID: $TARGET_PID)..."
kill -TERM "$TARGET_PID" >/dev/null 2>&1 || true
sleep 1
if kill -0 "$TARGET_PID" 2>/dev/null; then
    kill -KILL "$TARGET_PID" >/dev/null 2>&1 || true
fi
rm -f "$PID_FILE"
echo "Stop signal sent"
echo "[$(ts)] [stop.sh] stopped gateway pid=$TARGET_PID removed pid file"
echo "[$(ts)] [stop.sh] done"

