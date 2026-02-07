// Copyright 2025 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package launchlib

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// CreateDirectories ensures all directories specified in the config exist.
// Directories are created relative to the working directory (distribution root).
func CreateDirectories(dirs []string) error {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// WritePidFile writes the process ID to the specified file.
func WritePidFile(pid int, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create pid file directory %s: %w", dir, err)
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644)
}

// ReadPidFile reads a process ID from the specified file.
func ReadPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid in %s: %w", path, err)
	}
	return pid, nil
}

// RemovePidFile removes the PID file, ignoring errors if it doesn't exist.
func RemovePidFile(path string) {
	_ = os.Remove(path)
}

// SetResourceLimits applies OS-level resource limits before exec.
func SetResourceLimits(config ResourceConfig) error {
	if config.MaxOpenFiles > 0 {
		if err := setRlimit(syscall.RLIMIT_NOFILE, config.MaxOpenFiles); err != nil {
			return fmt.Errorf("failed to set RLIMIT_NOFILE to %d: %w", config.MaxOpenFiles, err)
		}
	}
	if config.MaxProcesses > 0 {
		if err := setRlimit(rlimitNproc, config.MaxProcesses); err != nil {
			return fmt.Errorf("failed to set RLIMIT_NPROC to %d: %w", config.MaxProcesses, err)
		}
	}
	if !config.CoreDumpEnabled {
		if err := setRlimit(syscall.RLIMIT_CORE, 0); err != nil {
			return fmt.Errorf("failed to disable core dumps: %w", err)
		}
	}
	return nil
}

func setRlimit(resource int, value uint64) error {
	limit := syscall.Rlimit{Cur: value, Max: value}
	return syscall.Setrlimit(resource, &limit)
}

// ResolveEnvVarPath resolves a path that may contain environment variable references.
// Supports both $VAR and ${VAR} syntax. If the referenced variable is not set,
// returns the path with the variable reference intact.
func ResolveEnvVarPath(path string) string {
	return os.ExpandEnv(path)
}

// BuildProcessEnv constructs the full environment for the Python process.
// Order of precedence (last wins):
//  1. Current process environment (inherited)
//  2. Memory management variables (from ComputeMemoryEnv)
//  3. Static config env
//  4. Custom config env (via MergedConfig)
//  5. SLS metadata variables (SLS_SERVICE_NAME, etc.)
func BuildProcessEnv(config MergedConfig, limits MemoryLimits, serviceName, serviceVersion string) []string {
	env := make(map[string]string)

	// Start with current environment
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}

	// Layer on memory management variables
	memEnv := BuildMemoryEnv(config, limits)
	for k, v := range memEnv {
		env[k] = v
	}

	// Layer on config-specified env (already merged static + custom)
	for k, v := range config.Env {
		env[k] = v
	}

	// Generic service metadata (always set)
	env["SERVICE_NAME"] = serviceName
	env["SERVICE_VERSION"] = serviceVersion

	// SLS metadata (kept for SLS-based deployments)
	env["SLS_SERVICE_NAME"] = serviceName
	env["SLS_SERVICE_VERSION"] = serviceVersion

	// Always set these Python best-practice variables unless explicitly overridden
	setDefault(env, "PYTHONDONTWRITEBYTECODE", "1")
	setDefault(env, "PYTHONUNBUFFERED", "1")

	// Set tmpdir
	setDefault(env, "TMPDIR", "var/data/tmp")

	// Convert back to []string
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

func setDefault(env map[string]string, key, value string) {
	if _, exists := env[key]; !exists {
		env[key] = value
	}
}

// BuildCommandArgs constructs the full command line based on LaunchMode.
//
// Supported modes:
//   - pex:      [pythonPath] [pythonOpts...] executable.pex [args...]
//   - module:   [pythonPath] [pythonOpts...] -m <executable> [args...]
//   - script:   [pythonPath] [pythonOpts...] <executable> [args...]
//   - uvicorn:  [pythonPath] [pythonOpts...] -m uvicorn <executable>:<entryPoint> [args...]
//   - gunicorn: [pythonPath] [pythonOpts...] -m gunicorn <executable>:<entryPoint> [args...]
//   - command:  <executable> [args...] (no Python wrapper)
func BuildCommandArgs(config MergedConfig) []string {
	switch config.LaunchMode {
	case LaunchModeCommand:
		return append([]string{config.Executable}, config.Args...)

	case LaunchModeModule:
		return buildPythonArgs(config, "-m", config.Executable)

	case LaunchModeScript:
		return buildPythonArgs(config, "", config.Executable)

	case LaunchModeUvicorn:
		appSpec := config.Executable
		if config.EntryPoint != "" {
			appSpec = config.Executable + ":" + config.EntryPoint
		}
		return buildPythonArgs(config, "-m", "uvicorn", appSpec)

	case LaunchModeGunicorn:
		appSpec := config.Executable
		if config.EntryPoint != "" {
			appSpec = config.Executable + ":" + config.EntryPoint
		}
		return buildPythonArgs(config, "-m", "gunicorn", appSpec)

	default: // LaunchModePEX or empty
		var args []string
		if config.PythonPath != "" {
			resolvedPython := ResolveEnvVarPath(config.PythonPath)
			args = append(args, resolvedPython)
			args = append(args, config.PythonOpts...)
			args = append(args, config.Executable)
		} else {
			args = append(args, config.Executable)
		}
		args = append(args, config.Args...)
		return args
	}
}

// buildPythonArgs is a helper that constructs [python] [opts...] [extraArgs...] [config.Args...]
func buildPythonArgs(config MergedConfig, extraArgs ...string) []string {
	var args []string
	pythonPath := config.PythonPath
	if pythonPath == "" {
		pythonPath = "python3"
	}
	args = append(args, ResolveEnvVarPath(pythonPath))
	args = append(args, config.PythonOpts...)
	for _, a := range extraArgs {
		if a != "" {
			args = append(args, a)
		}
	}
	args = append(args, config.Args...)
	return args
}

// ForwardSignals sets up signal forwarding from the launcher to the child process.
// SIGTERM and SIGINT are forwarded. SIGKILL cannot be caught or forwarded.
func ForwardSignals(pid int) chan os.Signal {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	go func() {
		for sig := range sigs {
			if sysSig, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(pid, sysSig)
			}
		}
	}()

	return sigs
}
