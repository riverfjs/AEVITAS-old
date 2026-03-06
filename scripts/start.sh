#!/bin/bash
# Start aevitas gateway in background with nohup

set -e

AEVITAS_BIN="${HOME}/.aevitas/bin/aevitas"
NOHUP_LOG="${HOME}/.aevitas/workspace/logs/nohup.out"
PID_FILE="${HOME}/.aevitas/aevitas.pid"
MAX_NOHUP_MB="${MAX_NOHUP_MB:-20}"

# Check if binary exists
if [ ! -f "$AEVITAS_BIN" ]; then
    echo "Error: aevitas binary not found at $AEVITAS_BIN"
    echo "Run 'make prod' to build and install"
    exit 1
fi

# Check if already running
if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if ps -p "$PID" > /dev/null 2>&1; then
        echo "aevitas gateway is already running (PID: $PID)"
        exit 1
    else
        # Stale PID file, remove it
        rm -f "$PID_FILE"
    fi
fi

# Ensure log directory exists
mkdir -p "$(dirname "$NOHUP_LOG")"

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

echo "aevitas gateway started (PID: $PID)"
echo "Main logs: ~/.aevitas/workspace/logs/aevitas.log"
echo "Startup logs: $NOHUP_LOG"

