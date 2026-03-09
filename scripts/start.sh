#!/bin/bash
# Start aevitas gateway in background with nohup

set -e

AEVITAS_BIN="${HOME}/.aevitas/bin/aevitas"
NOHUP_LOG="${HOME}/.aevitas/workspace/logs/nohup.out"
PID_FILE="${HOME}/.aevitas/aevitas.pid"
MAX_NOHUP_MB="${MAX_NOHUP_MB:-20}"

ts() { date "+%Y-%m-%dT%H:%M:%S%z"; }
echo "[$(ts)] [start.sh] begin"

# Check if binary exists
if [ ! -f "$AEVITAS_BIN" ]; then
    echo "Error: aevitas binary not found at $AEVITAS_BIN"
    echo "Run 'make prod' to build and install"
    exit 1
fi

# Idempotent start guard: if gateway is already running, do not spawn again.
if [ -f "$PID_FILE" ]; then
    EXISTING_PID="$(cat "$PID_FILE" 2>/dev/null || true)"
    case "$EXISTING_PID" in
        ''|*[!0-9]*) EXISTING_PID="" ;;
    esac
    if [ -n "$EXISTING_PID" ] && kill -0 "$EXISTING_PID" 2>/dev/null; then
        echo "aevitas gateway already running (PID: $EXISTING_PID)"
        echo "[$(ts)] [start.sh] already running pid=$EXISTING_PID, skip start"
        exit 0
    fi
    rm -f "$PID_FILE"
    echo "[$(ts)] [start.sh] removed stale pid file: $PID_FILE"
fi

# Fallback guard: pid file may be missing but gateway process still running.
EXISTING_GATEWAY_PID="$(pgrep -f "\\.aevitas/bin/aevitas gateway" | head -n 1 || true)"
if [ -n "$EXISTING_GATEWAY_PID" ]; then
    echo "aevitas gateway already running (PID: $EXISTING_GATEWAY_PID)"
    echo "$EXISTING_GATEWAY_PID" > "$PID_FILE"
    echo "[$(ts)] [start.sh] detected running gateway without pid file, restored pid file"
    exit 0
fi

# Ensure log directory exists
mkdir -p "$(dirname "$NOHUP_LOG")"
echo "[$(ts)] [start.sh] ensured log dir: $(dirname "$NOHUP_LOG")"

# Rotate nohup log when it grows too large.
# Keep one backup: nohup.out.1
if [ -f "$NOHUP_LOG" ]; then
    LOG_SIZE_BYTES=$(wc -c < "$NOHUP_LOG" 2>/dev/null || echo 0)
    # Guard against non-numeric output
    case "$LOG_SIZE_BYTES" in
        ''|*[!0-9]*) LOG_SIZE_BYTES=0 ;;
    esac
    MAX_BYTES=$((MAX_NOHUP_MB * 1024 * 1024))
    if [ "$LOG_SIZE_BYTES" -ge "$MAX_BYTES" ]; then
        mv -f "${NOHUP_LOG}.1" "${NOHUP_LOG}.2" 2>/dev/null || true
        mv -f "$NOHUP_LOG" "${NOHUP_LOG}.1"
        : > "$NOHUP_LOG"
        echo "Rotated nohup log: ${NOHUP_LOG} (>= ${MAX_NOHUP_MB}MB)"
    fi
fi

# Start in background with daemon mode
echo "Starting aevitas gateway..."
AEVITAS_DAEMON=1 nohup "$AEVITAS_BIN" gateway >> "$NOHUP_LOG" 2>&1 &
PID=$!

# Save PID
echo "$PID" > "$PID_FILE"
echo "[$(ts)] [start.sh] gateway spawned pid=$PID nohup_log=$NOHUP_LOG"

# Verify gateway is still alive shortly after spawn.
sleep 1
if ! kill -0 "$PID" 2>/dev/null; then
    rm -f "$PID_FILE"
    echo "[$(ts)] [start.sh] gateway exited early pid=$PID"
    echo "Error: aevitas gateway failed to stay running. Check: $NOHUP_LOG"
    exit 1
fi

echo "aevitas gateway started (PID: $PID)"
echo "Main logs: ~/.aevitas/workspace/logs/aevitas.log"
echo "Startup logs: $NOHUP_LOG"

echo "[$(ts)] [start.sh] done"

