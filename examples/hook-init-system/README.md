# Hook Init System

A composable container lifecycle framework using POSIX shell hooks.

## Lifecycle Phases

```
pre-configure.d → configure.d → pre-startup.d → startup.d →
post-startup.d  → [READY]     → (wait)        →
pre-shutdown.d  → shutdown.d  → [EXIT]
```

| Phase | Failure Policy |
|-------|---------------|
| `pre-configure` | halt (exit 1) |
| `configure` | halt (exit 1) |
| `pre-startup` | halt (exit 1) |
| `startup` | halt (exit 1) |
| `post-startup` | warn (continue degraded) |
| `pre-shutdown` | warn (best effort) |
| `shutdown` | warn (best effort) |

## Files

- **`entrypoint.sh`** — Container ENTRYPOINT that orchestrates the lifecycle
- **`hooks.sh`** — Core library providing `run_hooks`, `run_hooks_timed`, `run_hooks_warn`
- **`metrics-reporter.sh`** — Background resource metrics reporter for `post-startup.d`

## Usage

The SLS plugin integrates this automatically when `hooks={}` is set on an
`sls_service` target. The entrypoint and hooks library are embedded into
the distribution layout.

For standalone use, see the main hook init system documentation.
