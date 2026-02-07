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
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// WatchdogState tracks the current state of the RSS watchdog.
type WatchdogState int

const (
	WatchdogStateHealthy WatchdogState = iota
	WatchdogStateSoftWarning
	WatchdogStateHardLimit
	WatchdogStateTerminating
)

func (s WatchdogState) String() string {
	switch s {
	case WatchdogStateHealthy:
		return "healthy"
	case WatchdogStateSoftWarning:
		return "soft_warning"
	case WatchdogStateHardLimit:
		return "hard_limit"
	case WatchdogStateTerminating:
		return "terminating"
	default:
		return "unknown"
	}
}

// RSSWatchdog monitors the resident set size of a process and sends SIGTERM
// if it exceeds the configured threshold. This prevents the Linux OOM killer
// from sending SIGKILL, which doesn't allow graceful shutdown.
//
// The watchdog runs as a goroutine and reads /proc/[pid]/statm at a configurable
// interval. It transitions through states:
//
//	healthy -> soft_warning (log) -> hard_limit (SIGTERM) -> terminating (SIGKILL after grace)
type RSSWatchdog struct {
	pid    int
	limits MemoryLimits
	config WatchdogConfig
	logger *Logger
	state  WatchdogState

	// For testing: override the RSS reader
	readRSS func(pid int) (uint64, error)
}

// NewRSSWatchdog creates a new watchdog for the given process.
func NewRSSWatchdog(pid int, limits MemoryLimits, config WatchdogConfig, logger *Logger) *RSSWatchdog {
	return &RSSWatchdog{
		pid:     pid,
		limits:  limits,
		config:  config,
		logger:  logger,
		state:   WatchdogStateHealthy,
		readRSS: readProcessRSS,
	}
}

// Run starts the watchdog monitoring loop. It blocks until the context is
// cancelled or the process is terminated. Returns true if the watchdog
// triggered a termination.
func (w *RSSWatchdog) Run(ctx context.Context) bool {
	if w.limits.HardKillBytes == 0 {
		w.logger.Println("[watchdog] No memory limit configured, watchdog disabled")
		return false
	}

	interval := time.Duration(w.config.PollIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	w.logger.Printf("[watchdog] Started: pid=%d soft_warn=%s hard_kill=%s poll=%s grace=%ds",
		w.pid,
		formatBytes(w.limits.SoftWarnBytes),
		formatBytes(w.limits.HardKillBytes),
		interval,
		w.config.GracePeriodSeconds,
	)

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if triggered := w.check(); triggered {
				return true
			}
		}
	}
}

// check performs a single RSS check and transitions state if needed.
func (w *RSSWatchdog) check() bool {
	rss, err := w.readRSS(w.pid)
	if err != nil {
		// Process may have already exited
		w.logger.Printf("[watchdog] Failed to read RSS for pid %d: %v", w.pid, err)
		return false
	}

	switch {
	case rss >= w.limits.HardKillBytes && w.state < WatchdogStateHardLimit:
		w.state = WatchdogStateHardLimit
		w.logger.Printf("[watchdog] HARD LIMIT EXCEEDED: rss=%s limit=%s (%.1f%% of cgroup limit %s). Sending SIGTERM to pid %d.",
			formatBytes(rss),
			formatBytes(w.limits.HardKillBytes),
			float64(rss)/float64(w.limits.CgroupLimitBytes)*100,
			formatBytes(w.limits.CgroupLimitBytes),
			w.pid,
		)
		w.terminateProcess()
		return true

	case rss >= w.limits.SoftWarnBytes && w.state < WatchdogStateSoftWarning:
		w.state = WatchdogStateSoftWarning
		w.logger.Printf("[watchdog] SOFT WARNING: rss=%s warn_at=%s (%.1f%% of cgroup limit %s). "+
			"Process will be terminated at %s.",
			formatBytes(rss),
			formatBytes(w.limits.SoftWarnBytes),
			float64(rss)/float64(w.limits.CgroupLimitBytes)*100,
			formatBytes(w.limits.CgroupLimitBytes),
			formatBytes(w.limits.HardKillBytes),
		)

	case rss < w.limits.SoftWarnBytes && w.state == WatchdogStateSoftWarning:
		// RSS dropped back below soft warning threshold
		w.state = WatchdogStateHealthy
		w.logger.Printf("[watchdog] RSS recovered: rss=%s, back below soft warning threshold",
			formatBytes(rss))
	}

	return false
}

// terminateProcess sends SIGTERM followed by SIGKILL after the grace period.
func (w *RSSWatchdog) terminateProcess() {
	w.state = WatchdogStateTerminating

	// Send SIGTERM for graceful shutdown
	if err := syscall.Kill(w.pid, syscall.SIGTERM); err != nil {
		w.logger.Printf("[watchdog] Failed to send SIGTERM to pid %d: %v", w.pid, err)
		return
	}

	// Wait for grace period, then force kill
	go func() {
		grace := time.Duration(w.config.GracePeriodSeconds) * time.Second
		time.Sleep(grace)

		// Check if process is still alive
		if isProcessAlive(w.pid) {
			w.logger.Printf("[watchdog] Grace period (%s) expired, sending SIGKILL to pid %d",
				grace, w.pid)
			_ = syscall.Kill(w.pid, syscall.SIGKILL)
		}
	}()
}

// readProcessRSS reads the RSS of a process from /proc/[pid]/statm.
// The second field of statm is RSS in pages.
func readProcessRSS(pid int) (uint64, error) {
	path := fmt.Sprintf("/proc/%d/statm", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("failed to read %s: %w", path, err)
	}

	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected statm format: %q", string(data))
	}

	rssPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse RSS pages: %w", err)
	}

	pageSize := uint64(os.Getpagesize())
	return rssPages * pageSize, nil
}

// readProcessRSSWithChildren reads RSS for the process and all its children.
// This is important for Python because forking workers (gunicorn, multiprocessing)
// create child processes whose memory should count toward the total.
func readProcessRSSWithChildren(pid int) (uint64, error) {
	// Read the primary process RSS
	total, err := readProcessRSS(pid)
	if err != nil {
		return 0, err
	}

	// Read /proc/[pid]/task/[tid]/children for all child PIDs
	// Then recursively read their RSS
	childPids, err := getChildPids(pid)
	if err != nil {
		// Non-fatal: child enumeration may fail transiently
		return total, nil
	}

	for _, childPid := range childPids {
		childRSS, err := readProcessRSSWithChildren(childPid)
		if err != nil {
			continue // child may have exited
		}
		total += childRSS
	}

	return total, nil
}

// getChildPids returns the PIDs of all direct children of the given process.
func getChildPids(pid int) ([]int, error) {
	// Read /proc/[pid]/task/[pid]/children which contains space-separated child PIDs.
	path := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var pids []int
	for _, field := range strings.Fields(string(data)) {
		childPid, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		pids = append(pids, childPid)
	}
	return pids, nil
}

// isProcessAlive checks whether a process exists by sending signal 0.
func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// formatBytes returns a human-readable byte string.
func formatBytes(b uint64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)
	switch {
	case b >= GiB:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(GiB))
	case b >= MiB:
		return fmt.Sprintf("%.2f MiB", float64(b)/float64(MiB))
	case b >= KiB:
		return fmt.Sprintf("%.2f KiB", float64(b)/float64(KiB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
