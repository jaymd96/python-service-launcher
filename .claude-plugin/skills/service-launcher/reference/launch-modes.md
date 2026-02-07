# Launch Modes Reference

Six modes controlling how `BuildCommandArgs()` constructs the command line.

## pex (default)

Launches a PEX binary. If `pythonPath` is set, Python invokes the PEX. Otherwise the PEX is invoked directly (it has a shebang).

```yaml
launchMode: pex
executable: service/lib/myservice.pex
pythonPath: ""
args: ["--port", "8080"]
```

**With pythonPath**:
```
$pythonPath [pythonOpts...] service/lib/myservice.pex --port 8080
```

**Without pythonPath (direct)**:
```
service/lib/myservice.pex --port 8080
```

## module

Runs a Python module with `-m`.

```yaml
launchMode: module
executable: my_service.main
pythonPath: ""              # defaults to python3
args: ["--port", "8080"]
pythonOpts: ["-O"]
```

**Command**:
```
python3 -O -m my_service.main --port 8080
```

## script

Runs a Python script file.

```yaml
launchMode: script
executable: scripts/run.py
pythonPath: /usr/bin/python3.11
args: ["--config", "prod.yml"]
```

**Command**:
```
/usr/bin/python3.11 scripts/run.py --config prod.yml
```

## uvicorn

Runs an ASGI application via uvicorn. Combines `executable` and `entryPoint` into `module:callable` format.

```yaml
launchMode: uvicorn
executable: my_service.app
entryPoint: app
pythonPath: ""
args: ["--host", "0.0.0.0", "--port", "8080", "--workers", "4"]
```

**Command**:
```
python3 -m uvicorn my_service.app:app --host 0.0.0.0 --port 8080 --workers 4
```

If `entryPoint` is empty, just uses `executable` as the app spec:
```
python3 -m uvicorn my_service.app --host 0.0.0.0 --port 8080
```

## gunicorn

Runs a WSGI/ASGI application via gunicorn. Same `executable:entryPoint` pattern.

```yaml
launchMode: gunicorn
executable: my_service.wsgi
entryPoint: application
args: ["--bind", "0.0.0.0:8080", "--workers", "4"]
```

**Command**:
```
python3 -m gunicorn my_service.wsgi:application --bind 0.0.0.0:8080 --workers 4
```

## command

Runs an arbitrary executable without Python wrapping. No `pythonPath`, no `pythonOpts`.

```yaml
launchMode: command
executable: /usr/bin/nginx
args: ["-c", "/etc/nginx/nginx.conf"]
```

**Command**:
```
/usr/bin/nginx -c /etc/nginx/nginx.conf
```

## pythonPath Resolution

The `pythonPath` field supports environment variable expansion:

```yaml
pythonPath: $PYTHON_3_11_HOME/bin/python3
```

This is resolved using `os.ExpandEnv()` at launch time. Supports both `$VAR` and `${VAR}` syntax.

When `pythonPath` is empty:
- **pex mode**: PEX invoked directly (no interpreter prefix)
- **All other Python modes**: defaults to `"python3"`
- **command mode**: `pythonPath` is ignored entirely

## Environment Precedence

The full process environment is built in this order (last wins):

1. Current process environment (inherited from launcher)
2. Memory management variables (MEMORY_LIMIT_BYTES, MALLOC_*, etc.)
3. Config-specified env (static + custom merged)
4. Service metadata (SERVICE_NAME, SERVICE_VERSION, SLS_SERVICE_NAME, SLS_SERVICE_VERSION)
5. CPU env vars (OMP_NUM_THREADS, etc.) -- only set if not already overridden

Always set unless explicitly overridden:
- `PYTHONDONTWRITEBYTECODE=1`
- `PYTHONUNBUFFERED=1`
- `TMPDIR=var/data/tmp`
