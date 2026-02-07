# Memory Management Reference

## Cgroup Detection

### cgroup v2

Detected by presence of `/sys/fs/cgroup/cgroup.controllers`.

Memory limit read from `/sys/fs/cgroup/memory.max`:
- Numeric value -> limit in bytes
- `"max"` -> no limit, falls back to `/proc/meminfo` MemTotal

### cgroup v1

Detected by presence of `/sys/fs/cgroup/memory/memory.limit_in_bytes`.

Memory limit read from that file:
- Numeric value -> limit in bytes
- Value > 1 EiB (2^60) -> treated as unlimited, falls back to `/proc/meminfo` MemTotal

### Fallback

If neither cgroup version is detected and mode is `cgroup-aware`:
- **In container** (`CONTAINER` env set): hard error, launch fails
- **Outside container**: warning logged, falls back to `unmanaged` mode

## Effective Limit Formula

```
base = cgroupLimit * maxRssPercent / 100
effectiveLimit = base * (1 - heapFragmentationBuffer)
```

With defaults (`maxRssPercent=75`, `heapFragmentationBuffer=0.10`):
```
effectiveLimit = cgroupLimit * 0.75 * 0.90
               = cgroupLimit * 0.675
```

Example: 2 GiB container -> effective limit = 1.35 GiB

**Minimum floor**: 64 MiB. If the computed effective limit is below 64 MiB, it is clamped to 64 MiB.

## Watchdog Thresholds

Computed from the **cgroup limit** (not the effective limit):

```
softWarnBytes = cgroupLimit * softLimitPercent / 100    # Default: 85%
hardKillBytes = cgroupLimit * hardLimitPercent / 100     # Default: 95%
```

Example: 2 GiB container
- Soft warning at 1.7 GiB (85%)
- SIGTERM at 1.9 GiB (95%)
- OOM killer at 2 GiB (100%) -- the watchdog prevents reaching this

## Watchdog State Machine

```
healthy -> soft_warning -> hard_limit -> terminating
              |                              |
              v                              v
           healthy (recovery)            SIGKILL (after grace)
```

### States

| State | Trigger | Action |
|-------|---------|--------|
| `healthy` | RSS < soft threshold | Normal operation |
| `soft_warning` | RSS >= soft threshold | Log warning. Can recover if RSS drops. |
| `hard_limit` | RSS >= hard threshold | Send SIGTERM immediately |
| `terminating` | Grace period elapsed | Send SIGKILL if process still alive |

### Monitoring

The watchdog reads `/proc/[pid]/statm` every `pollIntervalSeconds` (default: 5). The second field is RSS in pages, multiplied by page size (typically 4096 bytes).

If reading `/proc/[pid]/statm` fails (process exited), the watchdog silently stops.

## Malloc Tuning

Set via environment variables to reduce memory fragmentation from C extensions:

### MALLOC_ARENA_MAX

Default: 2. Limits the number of glibc malloc arenas. Each arena can hold fragmented free memory that inflates RSS. Set to 0 for glibc default (8 * num_cpus).

### MALLOC_TRIM_THRESHOLD_

Default: 131072 (128 KB). Tells glibc to return free memory to the OS when a free chunk exceeds this size. Set to -1 to disable.

### PYTHONMALLOC=malloc

Forces Python to use the system allocator instead of pymalloc. This makes RSS more accurately reflect actual usage and allows glibc to return memory to the OS. Small performance cost for allocation-heavy workloads.

## Environment Variables Set

| Variable | Value | Notes |
|----------|-------|-------|
| `MEMORY_LIMIT_BYTES` | Effective limit | Available to application code |
| `CGROUP_LIMIT_BYTES` | Raw cgroup limit | For diagnostics |
| `MEMORY_MODE` | "cgroup-aware" or "fixed" | |
| `SLS_MEMORY_LIMIT_BYTES` | Same as MEMORY_LIMIT_BYTES | SLS backwards compat |
| `SLS_CGROUP_LIMIT_BYTES` | Same as CGROUP_LIMIT_BYTES | SLS backwards compat |
| `SLS_MEMORY_MODE` | Same as MEMORY_MODE | SLS backwards compat |
| `MALLOC_ARENA_MAX` | Default: 2 | glibc arena limit |
| `MALLOC_TRIM_THRESHOLD_` | Default: 131072 | glibc trim threshold |
| `PYTHONMALLOC` | "malloc" | System allocator |
| `OMP_NUM_THREADS` | CPU count | OpenMP threads |
| `MKL_NUM_THREADS` | CPU count | Intel MKL threads |
| `OPENBLAS_NUM_THREADS` | CPU count | OpenBLAS threads |
| `NUMEXPR_MAX_THREADS` | CPU count | numexpr threads |

## MemoryLimits Struct

```go
type MemoryLimits struct {
    CgroupLimitBytes    uint64  // Raw cgroup limit or system total
    EffectiveLimitBytes uint64  // Target RSS for the Python process
    SoftWarnBytes       uint64  // Watchdog warning threshold
    HardKillBytes       uint64  // Watchdog SIGTERM threshold
    CgroupVersion       int     // 1 or 2, or 0
    IsContainer         bool    // CONTAINER env var present
}
```
