# Config Reference

## StaticLauncherConfig

Build-time configuration at `service/bin/launcher-static.yml`.

```yaml
configType: python          # Must be "python" (or empty, defaults to "python")
configVersion: 1            # Must be 1

launchMode: pex             # pex | module | script | uvicorn | gunicorn | command
executable: service.pex     # Path to binary/script relative to dist root
pythonPath: ""              # Python interpreter path (supports $VAR expansion)
entryPoint: ""              # Override entry point (module:callable for uvicorn/gunicorn)
args: []                    # Arguments passed to the entry point
env: {}                     # Environment variables (key: value)
pythonOpts: []              # Python interpreter flags (e.g., -O, -u)

memory:
  mode: cgroup-aware        # cgroup-aware | fixed | unmanaged
  maxRssPercent: 75         # Target RSS as % of cgroup limit
  fixedLimitBytes: 0        # Only used when mode=fixed
  heapFragmentationBuffer: 0.10  # Subtracted for allocator overhead (10%)
  mallocTrimThreshold: 131072    # MALLOC_TRIM_THRESHOLD_ (128KB). -1 to disable.
  mallocArenaMax: 2         # MALLOC_ARENA_MAX. 0 for glibc default.

watchdog:
  enabled: true             # Active when memory mode is cgroup-aware or fixed
  pollIntervalSeconds: 5    # How often to check /proc/[pid]/statm
  softLimitPercent: 85      # Warning threshold (% of cgroup limit)
  hardLimitPercent: 95      # SIGTERM threshold (% of cgroup limit)
  gracePeriodSeconds: 30    # Wait after SIGTERM before SIGKILL

resources:
  maxOpenFiles: 65536       # RLIMIT_NOFILE
  maxProcesses: 4096        # RLIMIT_NPROC
  coreDumpEnabled: false    # RLIMIT_CORE (0 when false)

dirs: []                    # Directories to create before launch
                            # Default: ["var/data/tmp", "var/log", "var/run"]

subProcesses:               # Sidecar processes
  - name: ""                # Human-readable name
    executable: ""          # Path to binary
    args: []                # Arguments
    env: {}                 # Additional env vars

paths:
  staticConfig: ""          # Override: service/bin/launcher-static.yml
  pidFile: ""               # Override: var/run/%s.pid
  tmpDir: ""                # Override: var/data/tmp
  manifest: ""              # Override: deployment/manifest.yml

logging:
  format: text              # text | json
  level: info               # Log level
  fields: {}                # Extra fields for JSON log entries

readiness:
  enabled: false            # Enable readiness probe
  httpPort: 8081            # HTTP endpoint port
  httpPath: /ready          # HTTP endpoint path
  drainSeconds: 10          # Not-ready period before shutdown
  filePath: ""              # File to create when ready, remove on drain

cpu:
  autoDetect: true          # Read cgroup CPU quotas
  override: 0               # Explicit CPU count (0 = auto-detect)
```

## CustomLauncherConfig

Per-deployment overrides at `var/conf/launcher-custom.yml`. All fields optional.

```yaml
configType: python          # Must be "python" if present
configVersion: 1            # Must be 1 if present

env: {}                     # Merged with static (overrides on conflict)
pythonOpts: []              # Appended to static
args: []                    # Appended to static

memory:                     # Individual fields override static
  mode: ""
  maxRssPercent: 0
  fixedLimitBytes: 0
  heapFragmentationBuffer: 0
  mallocTrimThreshold: 0
  mallocArenaMax: 0

watchdog:                   # Individual fields override static
  enabled: null
  pollIntervalSeconds: 0
  softLimitPercent: 0
  hardLimitPercent: 0
  gracePeriodSeconds: 0

dangerousDisableContainerSupport: false  # Disables all container-aware behavior
```

## Merge Rules

| Field | Merge Strategy |
|-------|---------------|
| `env` | Static as base, custom overrides |
| `args` | Static + custom (appended) |
| `pythonOpts` | Static + custom (appended) |
| `memory.*` | Custom overrides individual fields (non-zero values only) |
| `watchdog.*` | Custom overrides individual fields (non-zero/non-nil values only) |
| All others | Static only (not overridable) |

## MergedConfig

The resolved config after merging, with computed fields:

```go
type MergedConfig struct {
    // All fields from static + custom merge
    LaunchMode, Executable, PythonPath, EntryPoint string
    Args, PythonOpts []string
    Env map[string]string
    Memory, Watchdog, Resources, Paths, Logging, Readiness, CPU ...

    // Computed at runtime
    EffectiveMemoryLimitBytes uint64  // From cgroup detection
    EffectiveCPUCount         int     // From CPU detection
    IsContainer               bool    // CONTAINER env var present
    CgroupVersion             int     // 1 or 2, 0 if not in container
}
```

## Default Detection

- `memory.mode`: defaults to `cgroup-aware` (applied in code defaults)
- Container detection: `CONTAINER` env var presence
- If `dangerousDisableContainerSupport: true`, `IsContainer` is forced to false
