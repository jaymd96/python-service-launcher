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
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	// cgroupV2MemoryMaxPath is the cgroup v2 memory limit file.
	cgroupV2MemoryMaxPath = "/sys/fs/cgroup/memory.max"

	// cgroupV1MemoryLimitPath is the cgroup v1 memory limit file.
	cgroupV1MemoryLimitPath = "/sys/fs/cgroup/memory/memory.limit_in_bytes"

	// cgroupV2IndicatorPath is used to detect cgroup v2.
	cgroupV2IndicatorPath = "/sys/fs/cgroup/cgroup.controllers"

	// procMemInfoPath is used as a fallback to get total system memory.
	procMemInfoPath = "/proc/meminfo"

	// minimumEffectiveLimitBytes is the absolute floor for memory limits.
	// Below this, Python itself may not start properly.
	minimumEffectiveLimitBytes = 64 * 1024 * 1024 // 64 MiB
)

// MemoryLimiter detects cgroup memory limits and computes effective limits
// for the Python process based on the launcher configuration.
type MemoryLimiter struct {
	filesystem fs.FS
}

// MemoryLimits holds the computed memory limits and associated metadata.
type MemoryLimits struct {
	// CgroupLimitBytes is the raw cgroup limit (or system total if not in a cgroup).
	CgroupLimitBytes uint64

	// EffectiveLimitBytes is the target RSS for the Python process,
	// computed as: CgroupLimitBytes * MaxRSSPercent/100 * (1 - HeapFragmentationBuffer)
	EffectiveLimitBytes uint64

	// SoftWarnBytes is the threshold at which the watchdog emits warnings.
	SoftWarnBytes uint64

	// HardKillBytes is the threshold at which the watchdog sends SIGTERM.
	HardKillBytes uint64

	// CgroupVersion is 1 or 2, or 0 if cgroups are not available.
	CgroupVersion int

	// IsContainer is true if the CONTAINER env var is set.
	IsContainer bool
}

// NewMemoryLimiter creates a new MemoryLimiter using the real filesystem.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{filesystem: os.DirFS("/")}
}

// NewMemoryLimiterWithFS creates a MemoryLimiter with an injected filesystem for testing.
func NewMemoryLimiterWithFS(filesystem fs.FS) *MemoryLimiter {
	return &MemoryLimiter{filesystem: filesystem}
}

// ComputeLimits determines the effective memory limits based on the merged config.
func (m *MemoryLimiter) ComputeLimits(config MergedConfig) (MemoryLimits, error) {
	limits := MemoryLimits{
		IsContainer: config.IsContainer,
	}

	switch config.Memory.Mode {
	case MemoryModeUnmanaged:
		return limits, nil

	case MemoryModeFixed:
		if config.Memory.FixedLimitBytes == 0 {
			return limits, fmt.Errorf("memory mode is 'fixed' but fixedLimitBytes is 0")
		}
		limits.CgroupLimitBytes = config.Memory.FixedLimitBytes

	case MemoryModeCgroupAware:
		cgroupVersion, err := m.detectCgroupVersion()
		if err != nil {
			return limits, fmt.Errorf("failed to detect cgroup version: %w", err)
		}
		limits.CgroupVersion = cgroupVersion

		cgroupLimit, err := m.readCgroupMemoryLimit(cgroupVersion)
		if err != nil {
			return limits, fmt.Errorf("failed to read cgroup memory limit: %w", err)
		}
		limits.CgroupLimitBytes = cgroupLimit

	default:
		return limits, fmt.Errorf("unknown memory mode: %q", config.Memory.Mode)
	}

	// Compute effective limit:
	//   base = cgroupLimit * maxRssPercent / 100
	//   effective = base * (1 - heapFragmentationBuffer)
	base := uint64(float64(limits.CgroupLimitBytes) * config.Memory.MaxRSSPercent / 100.0)
	effective := uint64(float64(base) * (1.0 - config.Memory.HeapFragmentationBuffer))

	if effective < minimumEffectiveLimitBytes {
		effective = minimumEffectiveLimitBytes
	}
	limits.EffectiveLimitBytes = effective

	// Compute watchdog thresholds (relative to the cgroup limit, not the effective limit,
	// because the watchdog monitors actual RSS against the real ceiling).
	limits.SoftWarnBytes = uint64(float64(limits.CgroupLimitBytes) * config.Watchdog.SoftLimitPercent / 100.0)
	limits.HardKillBytes = uint64(float64(limits.CgroupLimitBytes) * config.Watchdog.HardLimitPercent / 100.0)

	return limits, nil
}

// BuildMemoryEnv produces the environment variables that should be set based on
// the computed memory limits and config. These are merged into the process env.
func BuildMemoryEnv(config MergedConfig, limits MemoryLimits) map[string]string {
	env := make(map[string]string)

	if config.Memory.Mode == MemoryModeUnmanaged {
		return env
	}

	// Generic memory env vars
	env["MEMORY_LIMIT_BYTES"] = strconv.FormatUint(limits.EffectiveLimitBytes, 10)
	env["CGROUP_LIMIT_BYTES"] = strconv.FormatUint(limits.CgroupLimitBytes, 10)
	env["MEMORY_MODE"] = string(config.Memory.Mode)

	// SLS-prefixed aliases (kept for backwards compat in SLS deployments)
	env["SLS_MEMORY_LIMIT_BYTES"] = strconv.FormatUint(limits.EffectiveLimitBytes, 10)
	env["SLS_CGROUP_LIMIT_BYTES"] = strconv.FormatUint(limits.CgroupLimitBytes, 10)
	env["SLS_MEMORY_MODE"] = string(config.Memory.Mode)

	// glibc malloc tuning to reduce memory fragmentation.
	// Python's default allocator (pymalloc) handles small objects, but anything
	// that goes through C extensions (numpy, pandas, etc.) uses glibc malloc.
	if config.Memory.MallocArenaMax > 0 {
		env["MALLOC_ARENA_MAX"] = strconv.Itoa(config.Memory.MallocArenaMax)
	}
	if config.Memory.MallocTrimThreshold >= 0 {
		env["MALLOC_TRIM_THRESHOLD_"] = strconv.FormatInt(config.Memory.MallocTrimThreshold, 10)
	}

	// Use system malloc instead of pymalloc so that RSS more accurately reflects
	// actual usage and glibc can return memory to the OS. This has a small
	// performance cost for allocation-heavy workloads but dramatically improves
	// memory visibility for the watchdog.
	env["PYTHONMALLOC"] = "malloc"

	// Thread pool limiting is now handled by BuildCPUEnv in cpu.go.
	// For backwards compat, we still set these based on runtime.NumCPU()
	// if the caller doesn't use the CPU detection path.
	numCPU := strconv.Itoa(runtime.NumCPU())
	setDefaultMap(env, "OMP_NUM_THREADS", numCPU)
	setDefaultMap(env, "MKL_NUM_THREADS", numCPU)
	setDefaultMap(env, "OPENBLAS_NUM_THREADS", numCPU)
	setDefaultMap(env, "NUMEXPR_MAX_THREADS", numCPU)

	return env
}

// detectCgroupVersion determines whether the system uses cgroup v1 or v2.
func (m *MemoryLimiter) detectCgroupVersion() (int, error) {
	// cgroup v2 is indicated by the presence of cgroup.controllers at the root
	_, err := fs.Stat(m.filesystem, relPath(cgroupV2IndicatorPath))
	if err == nil {
		return 2, nil
	}

	// Check for cgroup v1 memory controller
	_, err = fs.Stat(m.filesystem, relPath(cgroupV1MemoryLimitPath))
	if err == nil {
		return 1, nil
	}

	return 0, fmt.Errorf("no cgroup memory controller found (checked v1 and v2 paths)")
}

// readCgroupMemoryLimit reads the memory limit from the appropriate cgroup path.
func (m *MemoryLimiter) readCgroupMemoryLimit(cgroupVersion int) (uint64, error) {
	var path string
	switch cgroupVersion {
	case 2:
		path = relPath(cgroupV2MemoryMaxPath)
	case 1:
		path = relPath(cgroupV1MemoryLimitPath)
	default:
		return 0, fmt.Errorf("unsupported cgroup version: %d", cgroupVersion)
	}

	data, err := fs.ReadFile(m.filesystem, path)
	if err != nil {
		return 0, fmt.Errorf("failed to read %s: %w", path, err)
	}

	content := strings.TrimSpace(string(data))

	// cgroup v2 uses "max" to indicate no limit
	if content == "max" {
		return m.readSystemMemory()
	}

	limit, err := strconv.ParseUint(content, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse memory limit %q: %w", content, err)
	}

	// cgroup v1 uses a very large number to indicate no limit
	// (typically 2^63 - 4096 or similar). Treat anything over 1 EiB as unlimited.
	if cgroupVersion == 1 && limit > 1<<60 {
		return m.readSystemMemory()
	}

	return limit, nil
}

// readSystemMemory reads total system memory from /proc/meminfo as a fallback.
func (m *MemoryLimiter) readSystemMemory() (uint64, error) {
	data, err := fs.ReadFile(m.filesystem, relPath(procMemInfoPath))
	if err != nil {
		return 0, fmt.Errorf("failed to read %s: %w", procMemInfoPath, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("failed to parse MemTotal: %w", err)
			}
			return kb * 1024, nil // convert kB to bytes
		}
	}

	return 0, fmt.Errorf("MemTotal not found in %s", procMemInfoPath)
}

// setDefaultMap sets a key in a map only if it's not already present.
func setDefaultMap(m map[string]string, key, value string) {
	if _, exists := m[key]; !exists {
		m[key] = value
	}
}

// relPath strips the leading "/" from an absolute path for use with fs.FS.
func relPath(absPath string) string {
	return filepath.Clean(strings.TrimPrefix(absPath, "/"))
}
