package launchlib

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const (
	// cgroupV2CPUMaxPath is the cgroup v2 CPU quota file.
	cgroupV2CPUMaxPath = "/sys/fs/cgroup/cpu.max"

	// cgroupV1CPUQuotaPath is the cgroup v1 CPU quota file.
	cgroupV1CPUQuotaPath = "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"

	// cgroupV1CPUPeriodPath is the cgroup v1 CPU period file.
	cgroupV1CPUPeriodPath = "/sys/fs/cgroup/cpu/cpu.cfs_period_us"
)

// CPUConfig controls CPU detection and thread pool sizing.
type CPUConfig struct {
	// AutoDetect reads cpu.max / cpu.cfs_quota_us and computes effective CPU count.
	// Default: true.
	AutoDetect bool `yaml:"autoDetect,omitempty"`

	// Override explicitly sets the CPU count. 0 means auto-detect.
	Override int `yaml:"override,omitempty"`
}

// DefaultCPUConfig returns sensible CPU defaults.
func DefaultCPUConfig() CPUConfig {
	return CPUConfig{AutoDetect: true}
}

// DetectCPUCount returns the effective number of CPUs available to the process.
// It reads cgroup CPU quotas when available, otherwise falls back to runtime.NumCPU().
func DetectCPUCount(config CPUConfig, filesystem fs.FS) int {
	if config.Override > 0 {
		return config.Override
	}
	if !config.AutoDetect {
		return runtime.NumCPU()
	}

	// Try cgroup v2 cpu.max
	count, err := readCgroupV2CPU(filesystem)
	if err == nil && count > 0 {
		return count
	}

	// Try cgroup v1 cpu.cfs_quota_us / cpu.cfs_period_us
	count, err = readCgroupV1CPU(filesystem)
	if err == nil && count > 0 {
		return count
	}

	return runtime.NumCPU()
}

// readCgroupV2CPU reads the CPU count from cgroup v2 cpu.max.
// Format: "$MAX $PERIOD" (e.g., "200000 100000" = 2 CPUs).
// "max 100000" means unlimited.
func readCgroupV2CPU(filesystem fs.FS) (int, error) {
	data, err := fs.ReadFile(filesystem, relPath(cgroupV2CPUMaxPath))
	if err != nil {
		return 0, err
	}
	content := strings.TrimSpace(string(data))
	fields := strings.Fields(content)
	if len(fields) != 2 {
		return 0, fmt.Errorf("unexpected cpu.max format: %q", content)
	}
	if fields[0] == "max" {
		return runtime.NumCPU(), nil // unlimited
	}
	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse cpu.max quota: %w", err)
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse cpu.max period: %w", err)
	}
	if period == 0 {
		return runtime.NumCPU(), nil
	}
	count := int(math.Ceil(quota / period))
	if count < 1 {
		count = 1
	}
	return count, nil
}

// readCgroupV1CPU reads CPU count from cgroup v1 quota/period files.
func readCgroupV1CPU(filesystem fs.FS) (int, error) {
	quotaData, err := fs.ReadFile(filesystem, relPath(cgroupV1CPUQuotaPath))
	if err != nil {
		return 0, err
	}
	quota, err := strconv.ParseFloat(strings.TrimSpace(string(quotaData)), 64)
	if err != nil {
		return 0, err
	}
	// -1 means unlimited
	if quota < 0 {
		return runtime.NumCPU(), nil
	}

	periodData, err := fs.ReadFile(filesystem, relPath(cgroupV1CPUPeriodPath))
	if err != nil {
		return 0, err
	}
	period, err := strconv.ParseFloat(strings.TrimSpace(string(periodData)), 64)
	if err != nil {
		return 0, err
	}
	if period == 0 {
		return runtime.NumCPU(), nil
	}

	count := int(math.Ceil(quota / period))
	if count < 1 {
		count = 1
	}
	return count, nil
}

// BuildCPUEnv produces CPU-related environment variables.
func BuildCPUEnv(cpuCount int) map[string]string {
	s := strconv.Itoa(cpuCount)
	return map[string]string{
		"OMP_NUM_THREADS":      s,
		"MKL_NUM_THREADS":      s,
		"OPENBLAS_NUM_THREADS":  s,
		"NUMEXPR_MAX_THREADS":  s,
		"SERVICE_CPU_COUNT":     s,
	}
}

// cpuFilesystem returns the FS to use for CPU detection.
func cpuFilesystem() fs.FS {
	return os.DirFS("/")
}
