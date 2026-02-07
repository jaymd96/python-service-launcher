#!/bin/sh
# entrypoint.sh — Container lifecycle entrypoint (POSIX sh compatible)
#
# Lifecycle:
#   pre-configure.d → configure.d → pre-startup.d → startup.d →
#   post-startup.d  → [READY]     → (wait)        →
#   pre-shutdown.d  → shutdown.d  → [EXIT]
#
# Startup is backgrounded; post-startup hooks run after.
# SIGTERM triggers graceful shutdown through the hook chain.
#
# Usage:
#   entrypoint.sh                      # default mode
#   SERVICE_MODE=worker entrypoint.sh   # mode-specific hooks

set -eu

# ---------------------------------------------------------------------------
# Resolve paths
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SERVICE_ROOT="${SERVICE_ROOT:-/opt/service}"

# Source the hook library
. "${SCRIPT_DIR}/../lib/hooks.sh"

# ---------------------------------------------------------------------------
# Directories
# ---------------------------------------------------------------------------
HOOK_BASE="${SERVICE_ROOT}/hooks"
HOOK_LOG_DIR="${SERVICE_ROOT}/var/logs"
HOOK_METRIC_DIR="${SERVICE_ROOT}/var/metrics"
HOOK_STATE_DIR="${SERVICE_ROOT}/var/state"

export HOOK_BASE HOOK_LOG_DIR HOOK_METRIC_DIR HOOK_STATE_DIR

# Service mode — allows same image to behave differently
SERVICE_MODE="${SERVICE_MODE:-default}"
export SERVICE_MODE

# PID tracking
MAIN_PID=""
SHUTDOWN_IN_PROGRESS=""

# ---------------------------------------------------------------------------
# Signal handling
# ---------------------------------------------------------------------------
_shutdown() {
    if [ -n "$SHUTDOWN_IN_PROGRESS" ]; then
        return
    fi
    SHUTDOWN_IN_PROGRESS=1

    _log "Shutdown signal received"

    # Pre-shutdown hooks (warn mode — best effort)
    run_hooks_warn "${HOOK_BASE}/pre-shutdown.d" \
        "${HOOK_LOG_DIR}/pre-shutdown.log"

    # Kill main process if running
    if [ -n "$MAIN_PID" ] && kill -0 "$MAIN_PID" 2>/dev/null; then
        _log "Sending TERM to main process (PID $MAIN_PID)"
        kill -TERM "$MAIN_PID" 2>/dev/null || true

        # Grace period
        _grace="${SHUTDOWN_GRACE_SECONDS:-10}"
        _waited=0
        while kill -0 "$MAIN_PID" 2>/dev/null && [ "$_waited" -lt "$_grace" ]; do
            sleep 1
            _waited=$(( _waited + 1 ))
        done

        # Force kill if still alive
        if kill -0 "$MAIN_PID" 2>/dev/null; then
            _log_err "Main process did not exit after ${_grace}s, sending KILL"
            kill -KILL "$MAIN_PID" 2>/dev/null || true
        fi
    fi

    # Shutdown hooks
    run_hooks_warn "${HOOK_BASE}/shutdown.d" \
        "${HOOK_LOG_DIR}/shutdown.log"

    # Clean state
    rm -f "${HOOK_STATE_DIR}/initialized"
    rm -f "${HOOK_STATE_DIR}/main.pid"

    _log "Shutdown complete"
    exit 0
}

trap _shutdown TERM INT

# ---------------------------------------------------------------------------
# Bootstrap
# ---------------------------------------------------------------------------
_ensure_dirs
mkdir -p "${HOOK_BASE}" 2>/dev/null || true

_log "=== Service starting (mode=${SERVICE_MODE}) ==="

# ---------------------------------------------------------------------------
# Phase 1: Pre-configure
# ---------------------------------------------------------------------------
_log "--- Phase: pre-configure ---"
run_hooks_timed "${HOOK_BASE}/pre-configure.d" \
    "${HOOK_LOG_DIR}/pre-configure.log" || {
    _log_err "Pre-configure failed, aborting"
    exit 1
}

# ---------------------------------------------------------------------------
# Phase 2: Configure
#
# If a sidecar injects environment.sh, source it here.
# ---------------------------------------------------------------------------
_log "--- Phase: configure ---"

_env_file="${SERVICE_ROOT}/var/environment.sh"
if [ -f "$_env_file" ]; then
    _log "Sourcing environment from sidecar: $_env_file"
    . "$_env_file"
fi

run_hooks_timed "${HOOK_BASE}/configure.d" \
    "${HOOK_LOG_DIR}/configure.log" || {
    _log_err "Configure failed, aborting"
    exit 1
}

# ---------------------------------------------------------------------------
# Phase 3: Pre-startup
# ---------------------------------------------------------------------------
_log "--- Phase: pre-startup ---"
run_hooks_timed "${HOOK_BASE}/pre-startup.d" \
    "${HOOK_LOG_DIR}/pre-startup.log" || {
    _log_err "Pre-startup failed, aborting"
    exit 1
}

# ---------------------------------------------------------------------------
# Phase 4: Startup
#
# Convention: The LAST script in startup.d should start the main process
# and write its PID to $HOOK_STATE_DIR/main.pid.
#
# If you only have one process, use startup.d/00-main.sh:
#   your-server &
#   echo $! > "$HOOK_STATE_DIR/main.pid"
# ---------------------------------------------------------------------------
_log "--- Phase: startup ---"
run_hooks_timed "${HOOK_BASE}/startup.d" \
    "${HOOK_LOG_DIR}/startup.log" || {
    _log_err "Startup failed, aborting"
    exit 1
}

# Pick up PID if the hook wrote one
if [ -f "${HOOK_STATE_DIR}/main.pid" ]; then
    MAIN_PID="$(cat "${HOOK_STATE_DIR}/main.pid")"
    _log "Main process PID: $MAIN_PID"
fi

# ---------------------------------------------------------------------------
# Phase 5: Post-startup
# ---------------------------------------------------------------------------
_log "--- Phase: post-startup ---"
run_hooks_timed "${HOOK_BASE}/post-startup.d" \
    "${HOOK_LOG_DIR}/post-startup.log" || {
    _log_err "Post-startup failed (service may be degraded)"
    # Don't abort — main process is already running
}

# ---------------------------------------------------------------------------
# Ready
# ---------------------------------------------------------------------------
touch "${HOOK_STATE_DIR}/initialized"
_log "=== Service ready ==="

# ---------------------------------------------------------------------------
# Wait
#
# If we have a main PID, wait on it. Otherwise wait forever (for signal).
# ---------------------------------------------------------------------------
if [ -n "$MAIN_PID" ] && kill -0 "$MAIN_PID" 2>/dev/null; then
    _log "Waiting on main process (PID $MAIN_PID)"
    wait "$MAIN_PID" 2>/dev/null || true
    _log "Main process exited"
    _shutdown
else
    _log "No main process — waiting for signal"
    # POSIX-portable idle loop (no `sleep infinity`)
    while true; do
        sleep 60 &
        wait $! 2>/dev/null || true
    done
fi
