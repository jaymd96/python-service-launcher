#!/bin/sh
# 99-metrics.sh — Background resource metrics reporter
#
# Writes JSON metrics to $HOOK_METRIC_DIR/resources.json every N seconds.
# Designed to be started from post-startup.d and run until shutdown.
set -eu

METRIC_INTERVAL="${METRIC_INTERVAL:-10}"
METRIC_FILE="${HOOK_METRIC_DIR:-/var/run/service/metrics}/resources.json"

_report() {
    # Memory from /proc (Linux)
    if [ -f /proc/meminfo ]; then
        _mem_total=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
        _mem_avail=$(awk '/MemAvailable/ {print $2}' /proc/meminfo)
        _mem_used=$(( _mem_total - _mem_avail ))
    else
        _mem_total=0
        _mem_avail=0
        _mem_used=0
    fi

    # Load average
    if [ -f /proc/loadavg ]; then
        _load="$(cut -d' ' -f1 /proc/loadavg)"
    else
        _load="0"
    fi

    # Disk usage of workspace
    _disk_used="$(df -k "${DATA_DIR:-/workspace}" 2>/dev/null | awk 'NR==2{print $3}' || echo 0)"

    # PID count for this service
    _main_pid_file="${HOOK_STATE_DIR:-/var/run/service/state}/main.pid"
    if [ -f "$_main_pid_file" ]; then
        _main_pid="$(cat "$_main_pid_file")"
        _children="$(pgrep -c -P "$_main_pid" 2>/dev/null || echo 0)"
    else
        _main_pid=""
        _children=0
    fi

    printf '{"ts":"%s","mem_used_kb":%d,"mem_total_kb":%d,"load":"%s","disk_used_kb":%s,"child_procs":%d}\n' \
        "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
        "$_mem_used" "$_mem_total" "$_load" "$_disk_used" "$_children" \
        > "$METRIC_FILE"
}

# Run in background — entrypoint will kill us on shutdown
while true; do
    _report
    sleep "$METRIC_INTERVAL"
done
