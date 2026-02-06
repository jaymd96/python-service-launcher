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
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultStaticConfigPath = "service/bin/launcher-static.yml"
	defaultCustomConfigPath = "var/conf/launcher-custom.yml"
	defaultCheckConfigPath  = "service/bin/launcher-check.yml"
)

// LauncherParams holds the parameters for a launch operation.
type LauncherParams struct {
	// ConfigDir is the root of the SLS distribution.
	// All relative paths in configs are resolved against this.
	DistRoot string

	// StaticConfigPath overrides the default static config location.
	StaticConfigPath string

	// CustomConfigPath overrides the default custom config location.
	CustomConfigPath string

	// ServiceName is used for PID files, logging, and env vars.
	ServiceName string

	// ServiceVersion is exposed as an env var to the process.
	ServiceVersion string

	// Stdout is where launcher output is written.
	Stdout io.Writer
}

// LaunchResult describes the outcome of a launch operation.
type LaunchResult struct {
	// ExitCode is the exit code of the child process. -1 if the process was signaled.
	ExitCode int

	// WatchdogTriggered is true if the watchdog sent SIGTERM due to memory pressure.
	WatchdogTriggered bool

	// Duration is how long the process ran.
	Duration time.Duration
}

// Launcher orchestrates the full lifecycle of launching a Python process:
//  1. Read and merge configs
//  2. Compute memory limits
//  3. Create required directories
//  4. Set resource limits
//  5. Build command and environment
//  6. Fork the process
//  7. Start the RSS watchdog
//  8. Forward signals
//  9. Wait for exit
type Launcher struct {
	params  LauncherParams
	logger  *log.Logger
	limiter *MemoryLimiter
}

// NewLauncher creates a new Launcher with the given parameters.
func NewLauncher(params LauncherParams) *Launcher {
	if params.Stdout == nil {
		params.Stdout = os.Stdout
	}
	if params.StaticConfigPath == "" {
		params.StaticConfigPath = defaultStaticConfigPath
	}
	if params.CustomConfigPath == "" {
		params.CustomConfigPath = defaultCustomConfigPath
	}
	logger := log.New(params.Stdout, "", log.LstdFlags|log.Lmicroseconds)
	return &Launcher{
		params:  params,
		logger:  logger,
		limiter: NewMemoryLimiter(),
	}
}

// Launch executes the full launch sequence and blocks until the process exits.
func (l *Launcher) Launch() (LaunchResult, error) {
	startTime := time.Now()

	l.logger.Printf("python-service-launcher starting (service=%s, version=%s)",
		l.params.ServiceName, l.params.ServiceVersion)

	// --- 1. Read and merge configs ---

	staticPath := l.resolvePath(l.params.StaticConfigPath)
	customPath := l.resolvePath(l.params.CustomConfigPath)

	staticConfig, customConfig, err := GetConfigsFromFiles(staticPath, customPath, l.params.Stdout)
	if err != nil {
		return LaunchResult{ExitCode: 1}, fmt.Errorf("config error: %w", err)
	}

	merged := MergeConfigs(staticConfig, customConfig)
	l.logConfig(merged)

	// --- 2. Compute memory limits ---

	limits, err := l.limiter.ComputeLimits(merged)
	if err != nil {
		// Memory limit detection failure is non-fatal in non-container environments.
		// In containers, it's a hard error because we need the watchdog.
		if merged.IsContainer {
			return LaunchResult{ExitCode: 1}, fmt.Errorf("memory limit detection failed in container: %w", err)
		}
		l.logger.Printf("WARNING: failed to detect memory limits: %v (continuing with unmanaged memory)", err)
		merged.Memory.Mode = MemoryModeUnmanaged
		limits = MemoryLimits{}
	}
	merged.EffectiveMemoryLimitBytes = limits.EffectiveLimitBytes

	if limits.EffectiveLimitBytes > 0 {
		l.logger.Printf("Memory limits: cgroup=%s effective=%s mode=%s",
			formatBytes(limits.CgroupLimitBytes),
			formatBytes(limits.EffectiveLimitBytes),
			merged.Memory.Mode,
		)
	}

	// --- 3. Create required directories ---

	dirs := merged.Dirs
	if len(dirs) == 0 {
		// Default directories matching go-java-launcher conventions
		dirs = []string{"var/data/tmp", "var/log", "var/run"}
	}
	if err := CreateDirectories(dirs); err != nil {
		return LaunchResult{ExitCode: 1}, fmt.Errorf("directory creation failed: %w", err)
	}

	// --- 4. Set resource limits ---

	if err := SetResourceLimits(merged.Resources); err != nil {
		l.logger.Printf("WARNING: failed to set resource limits: %v", err)
	}

	// --- 5. Build command and environment ---

	cmdArgs := BuildCommandArgs(merged)
	env := BuildProcessEnv(merged, limits, l.params.ServiceName, l.params.ServiceVersion)

	// Resolve the executable path
	executablePath := l.resolvePath(cmdArgs[0])
	cmdArgs[0] = executablePath

	l.logger.Printf("Launching: %s", strings.Join(cmdArgs, " "))

	// --- 6. Fork the process ---

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdout = l.params.Stdout
	cmd.Stderr = l.params.Stdout // merge stderr into stdout, same as go-java-launcher
	cmd.Env = env
	cmd.Dir = l.params.DistRoot

	if err := cmd.Start(); err != nil {
		return LaunchResult{ExitCode: 1}, fmt.Errorf("failed to start process: %w", err)
	}

	pid := cmd.Process.Pid
	l.logger.Printf("Process started: pid=%d", pid)

	// Write PID file
	pidPath := fmt.Sprintf("var/run/%s.pid", l.params.ServiceName)
	if err := WritePidFile(pid, pidPath); err != nil {
		l.logger.Printf("WARNING: failed to write pid file: %v", err)
	}
	defer RemovePidFile(pidPath)

	// --- 7. Start the RSS watchdog ---

	watchdogCtx, watchdogCancel := context.WithCancel(context.Background())
	defer watchdogCancel()

	watchdogTriggered := make(chan bool, 1)

	if merged.Memory.Mode != MemoryModeUnmanaged && merged.Watchdog.Enabled != nil && *merged.Watchdog.Enabled {
		watchdog := NewRSSWatchdog(pid, limits, merged.Watchdog, l.logger)
		go func() {
			triggered := watchdog.Run(watchdogCtx)
			watchdogTriggered <- triggered
		}()
	} else {
		watchdogTriggered <- false
	}

	// --- 8. Forward signals ---

	sigChan := ForwardSignals(pid)
	defer func() {
		close(sigChan)
	}()

	// --- 9. Launch subprocesses ---

	var subCmds []*exec.Cmd
	for _, sub := range merged.SubProcesses {
		subCmd := exec.Command(l.resolvePath(sub.Executable), sub.Args...)
		subCmd.Stdout = l.params.Stdout
		subCmd.Stderr = l.params.Stdout
		subCmd.Dir = l.params.DistRoot

		// Build subprocess env: inherit from parent, overlay subprocess-specific
		subEnv := make([]string, len(env))
		copy(subEnv, env)
		for k, v := range sub.Env {
			subEnv = append(subEnv, k+"="+v)
		}
		subCmd.Env = subEnv

		if err := subCmd.Start(); err != nil {
			l.logger.Printf("WARNING: failed to start subprocess %s: %v", sub.Name, err)
			continue
		}
		l.logger.Printf("Subprocess started: name=%s pid=%d", sub.Name, subCmd.Process.Pid)
		subCmds = append(subCmds, subCmd)
	}

	// --- 10. Wait for primary process exit ---

	waitErr := cmd.Wait()
	watchdogCancel() // stop the watchdog

	duration := time.Since(startTime)

	// Cleanup subprocesses
	for _, subCmd := range subCmds {
		if subCmd.Process != nil {
			_ = subCmd.Process.Kill()
			_ = subCmd.Wait()
		}
	}

	// Determine exit code
	result := LaunchResult{
		Duration: duration,
	}

	// Check if watchdog triggered
	select {
	case triggered := <-watchdogTriggered:
		result.WatchdogTriggered = triggered
	default:
		result.WatchdogTriggered = false
	}

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
		}
	} else {
		result.ExitCode = 0
	}

	l.logger.Printf("Process exited: code=%d duration=%s watchdog_triggered=%t",
		result.ExitCode, duration.Round(time.Millisecond), result.WatchdogTriggered)

	return result, nil
}

// resolvePath resolves a path relative to the distribution root.
func (l *Launcher) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(l.params.DistRoot, path)
}

// logConfig logs the resolved configuration for debugging.
func (l *Launcher) logConfig(config MergedConfig) {
	l.logger.Printf("Config: executable=%s entryPoint=%s pythonPath=%s",
		config.Executable, config.EntryPoint, config.PythonPath)
	l.logger.Printf("Config: memory.mode=%s memory.maxRssPercent=%.0f%% memory.fragBuffer=%.0f%%",
		config.Memory.Mode, config.Memory.MaxRSSPercent, config.Memory.HeapFragmentationBuffer*100)
	if len(config.Args) > 0 {
		l.logger.Printf("Config: args=%v", config.Args)
	}
	if config.Watchdog.Enabled != nil {
		l.logger.Printf("Config: watchdog.enabled=%t watchdog.poll=%ds watchdog.soft=%.0f%% watchdog.hard=%.0f%%",
			*config.Watchdog.Enabled,
			config.Watchdog.PollIntervalSeconds,
			config.Watchdog.SoftLimitPercent,
			config.Watchdog.HardLimitPercent,
		)
	}
	if config.IsContainer {
		l.logger.Println("Config: running in container mode")
	}
}
