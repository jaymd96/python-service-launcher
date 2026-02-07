#!/bin/sh
# hooks.sh — Core hook execution library (POSIX sh compatible)
#
# Provides:
#   run_hooks       <dir> [logfile]   — run all scripts in dir, halt on failure
#   run_hooks_timed <dir> [logfile]   — same, but emit timing metrics
#   run_hooks_warn  <dir> [logfile]   — run all, log failures but continue
#
# Hook scripts are executed in lexicographic order.
# Only files matching *.sh with +x are executed.
# Each hook receives the phase name via $HOOK_PHASE.

# ---------------------------------------------------------------------------
# Config — override via environment before sourcing
# ---------------------------------------------------------------------------
HOOK_BASE="${HOOK_BASE:-/opt/service}"
HOOK_LOG_DIR="${HOOK_LOG_DIR:-/var/run/service/logs}"
HOOK_METRIC_DIR="${HOOK_METRIC_DIR:-/var/run/service/metrics}"
HOOK_STATE_DIR="${HOOK_STATE_DIR:-/var/run/service/state}"

# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------

_log() {
    printf '[%s] [hooks] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"
}

_log_err() {
    _log "ERROR: $*" >&2
}

_millis() {
    # Portable epoch-seconds; milliseconds where date supports %N
    if date '+%s%N' >/dev/null 2>&1; then
        echo $(( $(date '+%s%N') / 1000000 ))
    else
        echo $(( $(date '+%s') * 1000 ))
    fi
}

_ensure_dirs() {
    mkdir -p "$HOOK_LOG_DIR" "$HOOK_METRIC_DIR" "$HOOK_STATE_DIR" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# run_hooks <dir> [logfile]
#
# Execute every *.sh file in <dir> in sorted order.
# Stops on first non-zero exit. Returns that exit code.
# ---------------------------------------------------------------------------
run_hooks() {
    _dir="$1"
    _logfile="${2:-/dev/null}"

    if [ ! -d "$_dir" ]; then
        _log "No hook directory: $_dir (skipping)"
        return 0
    fi

    # Collect executable .sh files, sorted
    _count=0
    for _script in "$_dir"/*.sh; do
        [ -f "$_script" ] || continue
        [ -x "$_script" ] || continue
        _count=$(( _count + 1 ))

        _name="$(basename "$_script")"
        _log "Running hook: $_name"

        _rc=0
        "$_script" >> "$_logfile" 2>&1 || _rc=$?
        if [ "$_rc" -ne 0 ]; then
            _log_err "Hook failed: $_name (exit $_rc)"
            return "$_rc"
        fi
    done

    if [ "$_count" -eq 0 ]; then
        _log "No hooks in $_dir"
    else
        _log "Completed $_count hook(s) from $_dir"
    fi

    return 0
}

# ---------------------------------------------------------------------------
# run_hooks_timed <dir> [logfile]
#
# Same as run_hooks but writes per-hook timing and a phase summary
# to $HOOK_METRIC_DIR/<phase>.json
# ---------------------------------------------------------------------------
run_hooks_timed() {
    _dir="$1"
    _logfile="${2:-/dev/null}"
    _phase="$(basename "$_dir" | sed 's/\.d$//')"

    _ensure_dirs

    if [ ! -d "$_dir" ]; then
        _log "No hook directory: $_dir (skipping)"
        return 0
    fi

    _phase_start="$(_millis)"
    _hook_timings=""
    _count=0
    _failed=""

    for _script in "$_dir"/*.sh; do
        [ -f "$_script" ] || continue
        [ -x "$_script" ] || continue
        _count=$(( _count + 1 ))

        _name="$(basename "$_script")"
        _log "Running hook: $_name"

        _hook_start="$(_millis)"

        _hook_rc=0
        "$_script" >> "$_logfile" 2>&1 || _hook_rc=$?

        _hook_end="$(_millis)"
        _hook_ms=$(( _hook_end - _hook_start ))

        # Accumulate JSON fragments
        [ -n "$_hook_timings" ] && _hook_timings="${_hook_timings},"
        _hook_timings="${_hook_timings}{\"hook\":\"${_name}\",\"ms\":${_hook_ms},\"rc\":${_hook_rc}}"

        if [ "$_hook_rc" -ne 0 ]; then
            _log_err "Hook failed: $_name (exit $_hook_rc, ${_hook_ms}ms)"
            _failed="$_name"
            break
        fi

        _log "Hook complete: $_name (${_hook_ms}ms)"
    done

    _phase_end="$(_millis)"
    _phase_ms=$(( _phase_end - _phase_start ))

    # Write metric
    if [ -n "$_failed" ]; then
        _status="failed"
    else
        _status="ok"
    fi

    printf '{"phase":"%s","status":"%s","total_ms":%d,"hooks":[%s]}\n' \
        "$_phase" "$_status" "$_phase_ms" "$_hook_timings" \
        > "${HOOK_METRIC_DIR}/${_phase}.json"

    if [ -n "$_failed" ]; then
        return 1
    fi

    _log "Phase $_phase complete: $_count hook(s) in ${_phase_ms}ms"
    return 0
}

# ---------------------------------------------------------------------------
# run_hooks_warn <dir> [logfile]
#
# Execute all hooks but do NOT halt on failure — log and continue.
# Returns 0 even if individual hooks fail.
# ---------------------------------------------------------------------------
run_hooks_warn() {
    _dir="$1"
    _logfile="${2:-/dev/null}"

    if [ ! -d "$_dir" ]; then
        _log "No hook directory: $_dir (skipping)"
        return 0
    fi

    _count=0
    _failures=0

    for _script in "$_dir"/*.sh; do
        [ -f "$_script" ] || continue
        [ -x "$_script" ] || continue
        _count=$(( _count + 1 ))

        _name="$(basename "$_script")"
        _log "Running hook: $_name"

        _rc=0
        "$_script" >> "$_logfile" 2>&1 || _rc=$?
        if [ "$_rc" -ne 0 ]; then
            _log_err "Hook failed (continuing): $_name (exit $_rc)"
            _failures=$(( _failures + 1 ))
        fi
    done

    if [ "$_failures" -gt 0 ]; then
        _log "Completed with $_failures failure(s) out of $_count hook(s)"
    fi

    return 0
}
