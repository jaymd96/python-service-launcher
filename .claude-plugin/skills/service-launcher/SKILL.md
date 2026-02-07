---
description: Container-aware Go launcher for Python services with memory management, RSS watchdog, and 6 launch modes
user_invocable: true
triggers:
  - "launcher"
  - "service launcher"
  - "python launcher"
  - "launcher config"
  - "launcher-static"
  - "watchdog"
  - "memory management"
  - "cgroup"
  - "pex launch"
---

# Service Launcher

You are an expert on `python-service-launcher` (v0.2.0), a Go binary that securely launches Python processes in containers. Use this knowledge to help developers configure and operate the launcher.

## Overview

A native Go binary that replaces shell-based launch scripts with:
- **6 launch modes**: pex, module, script, uvicorn, gunicorn, command
- **Cgroup-aware memory management**: detects container limits, sets env vars
- **RSS watchdog**: monitors memory and sends SIGTERM before OOM killer fires SIGKILL
- **CPU detection**: reads cgroup CPU quotas, sets thread pool env vars
- **Readiness probes**: HTTP and file-based health endpoints
- **Signal forwarding**: SIGTERM/SIGINT/SIGHUP forwarded to child process
- **Structured logging**: text or JSON format

## Architecture

Go packages in `launchlib/`:

| Package | Purpose |
|---------|---------|
| `config.go` | YAML config structs, merge logic, defaults |
| `launcher.go` | 11-step launch sequence orchestrator |
| `process.go` | Command construction, env building, signal forwarding |
| `memory.go` | Cgroup memory detection, env var generation |
| `watchdog.go` | RSS monitoring state machine |
| `cpu.go` | Cgroup CPU quota detection |
| `readiness.go` | HTTP + file readiness probes |
| `logging.go` | Text/JSON structured logger |

CLI entry point: `cmd/python-service-launcher/main.go`

## Config Files

Two YAML config files, merged at startup:

### launcher-static.yml (build-time, immutable)

Generated at build time by `pants-sls-distribution`. Lives at `service/bin/launcher-static.yml`.

```yaml
configType: python
configVersion: 1
launchMode: uvicorn
executable: my_service.app
entryPoint: app
args: ["--host", "0.0.0.0", "--port", "8080"]
pythonPath: ""
env:
  SOME_VAR: "value"
pythonOpts: ["-O"]
memory:
  mode: cgroup-aware
  maxRssPercent: 75
  heapFragmentationBuffer: 0.10
  mallocTrimThreshold: 131072
  mallocArenaMax: 2
watchdog:
  enabled: true
  pollIntervalSeconds: 5
  softLimitPercent: 85
  hardLimitPercent: 95
  gracePeriodSeconds: 30
resources:
  maxOpenFiles: 65536
  maxProcesses: 4096
  coreDumpEnabled: false
readiness:
  enabled: true
  httpPort: 8081
  httpPath: /ready
  drainSeconds: 10
cpu:
  autoDetect: true
logging:
  format: text
  level: info
```

### launcher-custom.yml (per-deployment, mutable)

Operators can override settings at deploy time. Lives at `var/conf/launcher-custom.yml`. Optional -- missing file is silently ignored.

```yaml
configType: python
configVersion: 1
env:
  EXTRA_VAR: "override"
args: ["--workers", "4"]
pythonOpts: ["-u"]
memory:
  maxRssPercent: 80
watchdog:
  softLimitPercent: 80
dangerousDisableContainerSupport: false
```

**Merge rules**: `env` is merged (custom overrides static). `args` and `pythonOpts` are appended. `memory` and `watchdog` fields override individually.

For full schema see [reference/config-reference.md](reference/config-reference.md).

## Launch Modes

Six modes controlling how the process command is constructed:

| Mode | Command Pattern |
|------|----------------|
| `pex` (default) | `[pythonPath] [pythonOpts...] executable.pex [args...]` |
| `module` | `python3 [pythonOpts...] -m <executable> [args...]` |
| `script` | `python3 [pythonOpts...] <executable> [args...]` |
| `uvicorn` | `python3 [pythonOpts...] -m uvicorn <executable>:<entryPoint> [args...]` |
| `gunicorn` | `python3 [pythonOpts...] -m gunicorn <executable>:<entryPoint> [args...]` |
| `command` | `<executable> [args...]` (no Python wrapper) |

For `pex` mode, if `pythonPath` is empty the PEX is invoked directly. For `module`/`script`/`uvicorn`/`gunicorn`, empty `pythonPath` defaults to `python3`.

For detailed examples see [reference/launch-modes.md](reference/launch-modes.md).

## Memory Management

Three modes controlled by `memory.mode`:

### cgroup-aware (default in containers)

Reads `/sys/fs/cgroup/memory.max` (v2) or `/sys/fs/cgroup/memory/memory.limit_in_bytes` (v1). Computes:

```
effectiveLimit = cgroupLimit * maxRssPercent/100 * (1 - heapFragmentationBuffer)
```

With defaults (75% RSS, 10% fragmentation buffer):
```
effectiveLimit = cgroupLimit * 0.75 * 0.90 = cgroupLimit * 0.675
```

Minimum effective limit: 64 MiB.

### fixed

Uses `memory.fixedLimitBytes` directly instead of reading cgroups. Same formula applies.

### unmanaged

No memory management. No watchdog. No memory env vars set.

### Environment Variables Set

When memory mode is not `unmanaged`, the launcher sets:

| Variable | Value |
|----------|-------|
| `MEMORY_LIMIT_BYTES` | Effective limit |
| `CGROUP_LIMIT_BYTES` | Raw cgroup limit |
| `MEMORY_MODE` | "cgroup-aware" or "fixed" |
| `MALLOC_ARENA_MAX` | Default: 2 |
| `MALLOC_TRIM_THRESHOLD_` | Default: 131072 |
| `PYTHONMALLOC` | "malloc" (use system allocator for RSS visibility) |
| `OMP_NUM_THREADS` | Detected CPU count |
| `MKL_NUM_THREADS` | Detected CPU count |
| `OPENBLAS_NUM_THREADS` | Detected CPU count |
| `NUMEXPR_MAX_THREADS` | Detected CPU count |

For full details see [reference/memory-management.md](reference/memory-management.md).

## RSS Watchdog

State machine monitoring `/proc/[pid]/statm`:

```
healthy -> soft_warning (log) -> hard_limit (SIGTERM) -> terminating (SIGKILL after grace)
```

- **healthy**: RSS below soft warning threshold
- **soft_warning**: RSS >= `softLimitPercent` of cgroup limit. Logs warning. Can recover back to healthy if RSS drops.
- **hard_limit**: RSS >= `hardLimitPercent` of cgroup limit. Sends SIGTERM immediately.
- **terminating**: After `gracePeriodSeconds`, sends SIGKILL if process still alive.

Watchdog is active when `memory.mode` is `cgroup-aware` or `fixed` and `watchdog.enabled` is true (default).

## CPU Detection

Reads cgroup CPU quotas to determine effective CPU count:
- **cgroup v2**: `/sys/fs/cgroup/cpu.max` format `"$MAX $PERIOD"` -> ceil(MAX/PERIOD)
- **cgroup v1**: `cpu.cfs_quota_us / cpu.cfs_period_us` -> ceil(quota/period)
- **Fallback**: `runtime.NumCPU()`

Sets `SERVICE_CPU_COUNT` and thread pool variables (`OMP_NUM_THREADS`, `MKL_NUM_THREADS`, `OPENBLAS_NUM_THREADS`, `NUMEXPR_MAX_THREADS`).

Can be overridden with `cpu.override` or disabled with `cpu.autoDetect: false`.

## Readiness Probes

Two mechanisms (can be used together):

### HTTP Probe
When `readiness.enabled: true`, serves an HTTP endpoint:
- **Ready**: `GET /ready` -> 200 OK
- **Not ready**: `GET /ready` -> 503 NOT READY
- Default port: 8081, path: `/ready`

### File Probe
When `readiness.filePath` is set:
- Creates the file when ready
- Removes the file during drain

### Drain Period
On shutdown, the probe reports not-ready for `drainSeconds` (default: 10) before the process exits. This allows load balancers to drain connections.

## CLI Usage

```bash
# Launch service (default)
python-service-launcher
python-service-launcher --startup

# Run health check
python-service-launcher --check

# Check if running
python-service-launcher --status

# Print version
python-service-launcher --version

# Override config paths
python-service-launcher --static-config path/to/static.yml --custom-config path/to/custom.yml

# Override dist root
python-service-launcher --dist-root /opt/services/my-service
```

The binary auto-detects the distribution root from its own path (3 levels up from `service/bin/<arch>/python-service-launcher`).

## The 11-Step Launch Sequence

1. **Read and merge configs** -- static + custom YAML
2. **Compute memory limits** -- cgroup detection or fixed
3. **Create required directories** -- `var/data/tmp`, `var/log`, `var/run` (or custom)
4. **Set resource limits** -- RLIMIT_NOFILE, RLIMIT_NPROC, RLIMIT_CORE
5. **Build command and environment** -- mode-specific command + full env
6. **Fork the process** -- `exec.Command` with merged env
7. **Start readiness probe** -- HTTP server + file marker
8. **Start RSS watchdog** -- background goroutine
9. **Forward signals** -- SIGTERM, SIGINT, SIGHUP -> child
10. **Launch subprocesses** -- sidecar processes
11. **Wait for primary process exit** -- cleanup watchdog, readiness, subprocesses

## Dev & Testing

```bash
go test ./... -v -race      # Run tests with race detector
go build ./cmd/python-service-launcher  # Build binary
```

## Bundling with pants-claude-plugins

This skill is automatically delivered to projects using `pants-sls-distribution`, which bundles it via `bundled_claude_plugins.py`. No extra setup needed -- run `pants claude-install --include-bundled ::`.

For standalone use outside SLS, declare the plugin in a BUILD file:

```python
claude_plugin(
    name="service-launcher",
    plugin="service-launcher",
    marketplace="service-launcher",
    scope="project",
)
```

## Gotchas

- The 11-step launch sequence is **sequential** -- if memory detection fails in a container, it's a hard error (step 2). Outside containers, it falls back to `unmanaged`.
- **Env precedence** (last wins): inherited env -> memory env -> static config env -> custom config env -> service metadata vars.
- **PYTHONDONTWRITEBYTECODE=1** and **PYTHONUNBUFFERED=1** are always set unless explicitly overridden in config env.
- **PYTHONMALLOC=malloc** is set when memory management is active -- this makes RSS more accurate but has a small performance cost for allocation-heavy workloads.
- **camelCase YAML keys** -- the config uses camelCase (e.g., `maxRssPercent`, `pollIntervalSeconds`) matching Go struct tags, not snake_case.
- The watchdog monitors the **primary process only** by default (reads `/proc/[pid]/statm`). There's also a `readProcessRSSWithChildren` function but the watchdog uses the simpler single-process reader.
- `TMPDIR` defaults to `var/data/tmp` (relative to dist root), not `/tmp`.
