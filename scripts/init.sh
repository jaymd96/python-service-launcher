#!/bin/bash
#
# Copyright 2025 Palantir Technologies, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
#
# SLS init script for Python services launched via python-service-launcher.
# Supports: start, stop, console, status, restart
#
# This script is placed at service/bin/init.sh in the SLS distribution.
# It delegates to the appropriate python-service-launcher binary for the current
# platform/architecture.

set -euo pipefail

# --- Resolve paths ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_DIR="$(dirname "$SCRIPT_DIR")"
DIST_ROOT="$(dirname "$SERVICE_DIR")"

# Detect platform and architecture for the correct python-service-launcher binary
detect_launcher() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *)
            echo "Unsupported architecture: $arch" >&2
            exit 1
            ;;
    esac

    local launcher="${SCRIPT_DIR}/${os}-${arch}/python-service-launcher"
    if [[ ! -x "$launcher" ]]; then
        echo "Launcher binary not found or not executable: $launcher" >&2
        exit 1
    fi
    echo "$launcher"
}

# Detect the service name from the manifest
detect_service_name() {
    local manifest="${DIST_ROOT}/deployment/manifest.yml"
    if [[ -f "$manifest" ]]; then
        grep -E '^product-name:' "$manifest" | sed 's/^product-name:[[:space:]]*//' | tr -d '"'"'"
    else
        # Fall back to directory name
        basename "$DIST_ROOT" | sed 's/-[0-9].*//'
    fi
}

LAUNCHER="$(detect_launcher)"
SERVICE_NAME="$(detect_service_name)"
PID_FILE="${DIST_ROOT}/var/run/${SERVICE_NAME}.pid"

# --- Commands ---

do_start() {
    if is_running; then
        echo "Service $SERVICE_NAME is already running (pid=$(cat "$PID_FILE"))"
        return 0
    fi

    echo "Starting $SERVICE_NAME..."

    # Ensure log and run directories exist
    mkdir -p "${DIST_ROOT}/var/log" "${DIST_ROOT}/var/run" "${DIST_ROOT}/var/data/tmp"

    # Launch in background, redirect output to log
    local log_file="${DIST_ROOT}/var/log/${SERVICE_NAME}-startup.log"

    cd "$DIST_ROOT"
    nohup "$LAUNCHER" \
        --service-name "$SERVICE_NAME" \
        > "$log_file" 2>&1 &

    local pid=$!
    disown "$pid"

    # The launcher writes its own PID file for the Python process,
    # but we also track the launcher PID for the init script.
    echo "$pid" > "$PID_FILE"

    # Wait briefly and check that it started
    sleep 1
    if is_running; then
        echo "Started $SERVICE_NAME (pid=$pid)"
    else
        echo "Failed to start $SERVICE_NAME. Check $log_file for details." >&2
        return 1
    fi
}

do_stop() {
    if ! is_running; then
        echo "Service $SERVICE_NAME is not running"
        rm -f "$PID_FILE"
        return 0
    fi

    local pid
    pid="$(cat "$PID_FILE")"
    echo "Stopping $SERVICE_NAME (pid=$pid)..."

    # Send SIGTERM and wait up to 30 seconds for graceful shutdown
    kill -TERM "$pid" 2>/dev/null || true

    local waited=0
    while is_running && [[ $waited -lt 30 ]]; do
        sleep 1
        waited=$((waited + 1))
    done

    if is_running; then
        echo "Graceful shutdown timed out after ${waited}s, sending SIGKILL"
        kill -KILL "$pid" 2>/dev/null || true
        sleep 1
    fi

    rm -f "$PID_FILE"
    echo "Stopped $SERVICE_NAME"
}

do_console() {
    # Run in foreground (no backgrounding)
    if is_running; then
        echo "Service $SERVICE_NAME is already running (pid=$(cat "$PID_FILE"))" >&2
        return 1
    fi

    echo "Starting $SERVICE_NAME in console mode..."
    cd "$DIST_ROOT"
    exec "$LAUNCHER" --service-name "$SERVICE_NAME"
}

do_status() {
    if is_running; then
        local pid
        pid="$(cat "$PID_FILE")"
        echo "Service $SERVICE_NAME is running (pid=$pid)"
        return 0
    else
        echo "Service $SERVICE_NAME is not running"
        rm -f "$PID_FILE" 2>/dev/null
        return 1
    fi
}

do_restart() {
    do_stop
    do_start
}

is_running() {
    if [[ ! -f "$PID_FILE" ]]; then
        return 1
    fi

    local pid
    pid="$(cat "$PID_FILE")"

    if [[ -z "$pid" ]]; then
        return 1
    fi

    # Check if process exists
    if kill -0 "$pid" 2>/dev/null; then
        return 0
    else
        return 1
    fi
}

# --- Main ---

case "${1:-}" in
    start)   do_start ;;
    stop)    do_stop ;;
    console) do_console ;;
    status)  do_status ;;
    restart) do_restart ;;
    *)
        echo "Usage: $0 {start|stop|console|status|restart}" >&2
        exit 1
        ;;
esac
