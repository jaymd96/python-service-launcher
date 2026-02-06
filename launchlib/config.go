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
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// ConfigType enumerates the supported launcher configuration types.
const (
	ConfigTypePython = "python"
)

// MemoryMode controls how the launcher manages memory limits for the Python process.
type MemoryMode string

const (
	// MemoryModeCgroupAware reads cgroup limits and sets up a watchdog to enforce
	// a target RSS percentage, sending SIGTERM before the OOM killer fires SIGKILL.
	MemoryModeCgroupAware MemoryMode = "cgroup-aware"

	// MemoryModeFixed uses explicitly provided byte limits without reading cgroups.
	MemoryModeFixed MemoryMode = "fixed"

	// MemoryModeUnmanaged disables all memory management. The process is on its own.
	MemoryModeUnmanaged MemoryMode = "unmanaged"
)

// StaticLauncherConfig represents the immutable configuration generated at build time.
// This is written to service/bin/launcher-static.yml by the Pants SLS packaging plugin
// and should never be modified after distribution.
type StaticLauncherConfig struct {
	// ConfigType must be "python".
	ConfigType string `yaml:"configType" validate:"nonzero"`

	// ConfigVersion must be 1.
	ConfigVersion int `yaml:"configVersion" validate:"nonzero"`

	// Executable is the path to the PEX binary, relative to the distribution root.
	// Example: "service/bin/my-service.pex"
	Executable string `yaml:"executable" validate:"nonzero"`

	// PythonPath optionally specifies the path to a Python interpreter.
	// If empty, the PEX's internal interpreter constraints are used.
	// Supports environment variable references like "$PYTHON_3_11_HOME/bin/python3".
	PythonPath string `yaml:"pythonPath,omitempty"`

	// EntryPoint optionally overrides the PEX's baked-in entry point.
	// Format: "module.path:callable" (e.g., "my_service.server:main").
	// If empty, the PEX's default entry point is used.
	EntryPoint string `yaml:"entryPoint,omitempty"`

	// Args are arguments passed to the Python entry point after the PEX is invoked.
	Args []string `yaml:"args,omitempty"`

	// Env specifies environment variables set before launching the process.
	// These cannot reference each other or use shell expansion.
	Env map[string]string `yaml:"env,omitempty"`

	// PythonOpts are flags passed to the Python interpreter itself (before the PEX path).
	// Examples: ["-O", "-u", "-W", "error"]
	// Note: most of these should be set via env vars (PYTHONOPTIMIZE, PYTHONUNBUFFERED)
	// but some flags have no env var equivalent.
	PythonOpts []string `yaml:"pythonOpts,omitempty"`

	// Memory configures how the launcher manages the process's memory footprint.
	Memory MemoryConfig `yaml:"memory,omitempty"`

	// Resources configures OS-level resource limits applied before exec.
	Resources ResourceConfig `yaml:"resources,omitempty"`

	// Dirs lists directories to create (relative to distribution root) before launch.
	Dirs []string `yaml:"dirs,omitempty"`

	// Watchdog configures the RSS monitoring watchdog.
	// Only active when Memory.Mode is "cgroup-aware" or "fixed".
	Watchdog WatchdogConfig `yaml:"watchdog,omitempty"`

	// SubProcesses defines additional processes launched alongside the primary.
	// Useful for sidecar patterns within a single SLS service (metrics exporters, etc).
	SubProcesses []SubProcessConfig `yaml:"subProcesses,omitempty"`
}

// MemoryConfig controls memory limit detection and enforcement.
type MemoryConfig struct {
	// Mode determines the memory management strategy.
	// Default: "cgroup-aware" when CONTAINER env var is present, "unmanaged" otherwise.
	Mode MemoryMode `yaml:"mode,omitempty"`

	// MaxRSSPercent is the target RSS as a percentage of the detected or fixed memory limit.
	// Default: 75. Only used when Mode is "cgroup-aware".
	MaxRSSPercent float64 `yaml:"maxRssPercent,omitempty"`

	// FixedLimitBytes is an explicit memory ceiling in bytes.
	// Only used when Mode is "fixed".
	FixedLimitBytes uint64 `yaml:"fixedLimitBytes,omitempty"`

	// HeapFragmentationBuffer is subtracted from the target to account for
	// Python's memory allocator fragmentation and overhead from native extensions.
	// Default: 0.10 (10%). The effective limit becomes:
	//   effectiveLimit = detectedLimit * MaxRSSPercent/100 * (1 - HeapFragmentationBuffer)
	HeapFragmentationBuffer float64 `yaml:"heapFragmentationBuffer,omitempty"`

	// MallocTrimThreshold sets MALLOC_TRIM_THRESHOLD_ to encourage glibc to
	// return memory to the OS. Default: 131072 (128KB). Set to -1 to disable.
	MallocTrimThreshold int64 `yaml:"mallocTrimThreshold,omitempty"`

	// MallocArenaMax sets MALLOC_ARENA_MAX to limit the number of glibc arenas.
	// Each arena can hold fragmented free memory that inflates RSS.
	// Default: 2. Set to 0 to use glibc default (8 * num_cpus).
	MallocArenaMax int `yaml:"mallocArenaMax,omitempty"`
}

// WatchdogConfig controls the RSS monitoring goroutine that prevents OOM kills.
type WatchdogConfig struct {
	// Enabled controls whether the watchdog runs. Default: true when memory mode
	// is "cgroup-aware" or "fixed".
	Enabled *bool `yaml:"enabled,omitempty"`

	// PollIntervalSeconds is how often the watchdog checks /proc/[pid]/statm.
	// Default: 5.
	PollIntervalSeconds int `yaml:"pollIntervalSeconds,omitempty"`

	// SoftLimitPercent triggers a warning log when RSS exceeds this percentage
	// of the effective memory limit. Default: 85.
	SoftLimitPercent float64 `yaml:"softLimitPercent,omitempty"`

	// HardLimitPercent triggers SIGTERM when RSS exceeds this percentage
	// of the effective memory limit. Default: 95.
	HardLimitPercent float64 `yaml:"hardLimitPercent,omitempty"`

	// GracePeriodSeconds is how long to wait after SIGTERM before sending SIGKILL.
	// Default: 30.
	GracePeriodSeconds int `yaml:"gracePeriodSeconds,omitempty"`
}

// ResourceConfig specifies OS-level resource limits set via setrlimit before exec.
type ResourceConfig struct {
	// MaxOpenFiles sets RLIMIT_NOFILE. Default: 65536.
	MaxOpenFiles uint64 `yaml:"maxOpenFiles,omitempty"`

	// MaxProcesses sets RLIMIT_NPROC. Default: 4096.
	MaxProcesses uint64 `yaml:"maxProcesses,omitempty"`

	// CoreDumpEnabled controls whether core dumps are permitted. Default: false.
	CoreDumpEnabled bool `yaml:"coreDumpEnabled,omitempty"`
}

// SubProcessConfig defines a sidecar process launched alongside the primary.
type SubProcessConfig struct {
	// Name is a human-readable identifier for logging.
	Name string `yaml:"name" validate:"nonzero"`

	// Executable is the path to the binary, relative to the distribution root.
	Executable string `yaml:"executable" validate:"nonzero"`

	// Args passed to the executable.
	Args []string `yaml:"args,omitempty"`

	// Env specifies additional environment variables for this subprocess.
	Env map[string]string `yaml:"env,omitempty"`
}

// CustomLauncherConfig represents the mutable configuration that operators can
// modify per-deployment. This is read from var/conf/launcher-custom.yml.
type CustomLauncherConfig struct {
	// ConfigType must be "python" if present.
	ConfigType string `yaml:"configType,omitempty"`

	// ConfigVersion must be 1 if present.
	ConfigVersion int `yaml:"configVersion,omitempty"`

	// Env specifies additional environment variables. These are merged with
	// (and override) the static config's env.
	Env map[string]string `yaml:"env,omitempty"`

	// PythonOpts are appended to the static config's PythonOpts.
	PythonOpts []string `yaml:"pythonOpts,omitempty"`

	// Args are appended to the static config's Args.
	// Note: later args typically override earlier args for most Python CLI frameworks.
	Args []string `yaml:"args,omitempty"`

	// Memory overrides for the memory configuration.
	Memory *MemoryConfig `yaml:"memory,omitempty"`

	// Watchdog overrides for the watchdog configuration.
	Watchdog *WatchdogConfig `yaml:"watchdog,omitempty"`

	// DangerousDisableContainerSupport disables all container-aware behavior.
	// This is equivalent to the go-java-launcher flag of the same name.
	DangerousDisableContainerSupport bool `yaml:"dangerousDisableContainerSupport,omitempty"`
}

// MergedConfig is the resolved configuration after combining static and custom configs.
type MergedConfig struct {
	Executable  string
	PythonPath  string
	EntryPoint  string
	Args        []string
	Env         map[string]string
	PythonOpts  []string
	Memory      MemoryConfig
	Watchdog    WatchdogConfig
	Resources   ResourceConfig
	Dirs        []string
	SubProcesses []SubProcessConfig

	// Computed fields
	EffectiveMemoryLimitBytes uint64
	IsContainer               bool
	CgroupVersion             int // 1 or 2, 0 if not in container
}

// DefaultMemoryConfig returns sensible defaults for memory management.
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		Mode:                    MemoryModeCgroupAware,
		MaxRSSPercent:           75,
		HeapFragmentationBuffer: 0.10,
		MallocTrimThreshold:     131072,
		MallocArenaMax:          2,
	}
}

// DefaultWatchdogConfig returns sensible defaults for the RSS watchdog.
func DefaultWatchdogConfig() WatchdogConfig {
	enabled := true
	return WatchdogConfig{
		Enabled:             &enabled,
		PollIntervalSeconds: 5,
		SoftLimitPercent:    85,
		HardLimitPercent:    95,
		GracePeriodSeconds:  30,
	}
}

// DefaultResourceConfig returns sensible defaults for resource limits.
func DefaultResourceConfig() ResourceConfig {
	return ResourceConfig{
		MaxOpenFiles: 65536,
		MaxProcesses: 4096,
	}
}

// GetConfigsFromFiles reads and parses both configuration files.
// The custom config file is optional and will be silently ignored if absent.
func GetConfigsFromFiles(
	staticConfigFile string,
	customConfigFile string,
	stdout io.Writer,
) (StaticLauncherConfig, CustomLauncherConfig, error) {

	staticConfig, err := readStaticConfig(staticConfigFile)
	if err != nil {
		return StaticLauncherConfig{}, CustomLauncherConfig{}, fmt.Errorf(
			"failed to read static config from %s: %w", staticConfigFile, err)
	}

	customConfig, err := readCustomConfig(customConfigFile, stdout)
	if err != nil {
		return StaticLauncherConfig{}, CustomLauncherConfig{}, fmt.Errorf(
			"failed to read custom config from %s: %w", customConfigFile, err)
	}

	if err := validateStaticConfig(staticConfig); err != nil {
		return StaticLauncherConfig{}, CustomLauncherConfig{}, fmt.Errorf(
			"invalid static config: %w", err)
	}

	return staticConfig, customConfig, nil
}

// MergeConfigs combines the static and custom configurations into a single resolved config.
func MergeConfigs(
	static StaticLauncherConfig,
	custom CustomLauncherConfig,
) MergedConfig {
	merged := MergedConfig{
		Executable:   static.Executable,
		PythonPath:   static.PythonPath,
		EntryPoint:   static.EntryPoint,
		Args:         append(append([]string{}, static.Args...), custom.Args...),
		PythonOpts:   append(append([]string{}, static.PythonOpts...), custom.PythonOpts...),
		Memory:       mergeMemoryConfig(static.Memory, custom.Memory),
		Watchdog:     mergeWatchdogConfig(static.Watchdog, custom.Watchdog),
		Resources:    static.Resources,
		Dirs:         static.Dirs,
		SubProcesses: static.SubProcesses,
	}

	// Merge environment: static as base, custom overrides
	merged.Env = make(map[string]string)
	for k, v := range static.Env {
		merged.Env[k] = v
	}
	for k, v := range custom.Env {
		merged.Env[k] = v
	}

	// Detect container environment
	_, merged.IsContainer = os.LookupEnv("CONTAINER")
	if custom.DangerousDisableContainerSupport {
		merged.IsContainer = false
	}

	return merged
}

func readStaticConfig(path string) (StaticLauncherConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StaticLauncherConfig{}, err
	}
	var config StaticLauncherConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return StaticLauncherConfig{}, err
	}
	return config, nil
}

func readCustomConfig(path string, stdout io.Writer) (CustomLauncherConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stdout, "Custom config file %s not found, using defaults\n", path)
			return CustomLauncherConfig{}, nil
		}
		return CustomLauncherConfig{}, err
	}
	var config CustomLauncherConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return CustomLauncherConfig{}, err
	}
	return config, nil
}

func validateStaticConfig(config StaticLauncherConfig) error {
	if config.ConfigType != ConfigTypePython {
		return fmt.Errorf("expected configType %q, got %q", ConfigTypePython, config.ConfigType)
	}
	if config.ConfigVersion != 1 {
		return fmt.Errorf("expected configVersion 1, got %d", config.ConfigVersion)
	}
	if config.Executable == "" {
		return fmt.Errorf("executable must not be empty")
	}
	return nil
}

func mergeMemoryConfig(static MemoryConfig, custom *MemoryConfig) MemoryConfig {
	result := static
	if custom == nil {
		return applyMemoryDefaults(result)
	}
	if custom.Mode != "" {
		result.Mode = custom.Mode
	}
	if custom.MaxRSSPercent > 0 {
		result.MaxRSSPercent = custom.MaxRSSPercent
	}
	if custom.FixedLimitBytes > 0 {
		result.FixedLimitBytes = custom.FixedLimitBytes
	}
	if custom.HeapFragmentationBuffer > 0 {
		result.HeapFragmentationBuffer = custom.HeapFragmentationBuffer
	}
	if custom.MallocTrimThreshold != 0 {
		result.MallocTrimThreshold = custom.MallocTrimThreshold
	}
	if custom.MallocArenaMax != 0 {
		result.MallocArenaMax = custom.MallocArenaMax
	}
	return applyMemoryDefaults(result)
}

func mergeWatchdogConfig(static WatchdogConfig, custom *WatchdogConfig) WatchdogConfig {
	result := static
	if custom == nil {
		return applyWatchdogDefaults(result)
	}
	if custom.Enabled != nil {
		result.Enabled = custom.Enabled
	}
	if custom.PollIntervalSeconds > 0 {
		result.PollIntervalSeconds = custom.PollIntervalSeconds
	}
	if custom.SoftLimitPercent > 0 {
		result.SoftLimitPercent = custom.SoftLimitPercent
	}
	if custom.HardLimitPercent > 0 {
		result.HardLimitPercent = custom.HardLimitPercent
	}
	if custom.GracePeriodSeconds > 0 {
		result.GracePeriodSeconds = custom.GracePeriodSeconds
	}
	return applyWatchdogDefaults(result)
}

func applyMemoryDefaults(config MemoryConfig) MemoryConfig {
	defaults := DefaultMemoryConfig()
	if config.Mode == "" {
		config.Mode = defaults.Mode
	}
	if config.MaxRSSPercent == 0 {
		config.MaxRSSPercent = defaults.MaxRSSPercent
	}
	if config.HeapFragmentationBuffer == 0 {
		config.HeapFragmentationBuffer = defaults.HeapFragmentationBuffer
	}
	if config.MallocTrimThreshold == 0 {
		config.MallocTrimThreshold = defaults.MallocTrimThreshold
	}
	if config.MallocArenaMax == 0 {
		config.MallocArenaMax = defaults.MallocArenaMax
	}
	return config
}

func applyWatchdogDefaults(config WatchdogConfig) WatchdogConfig {
	defaults := DefaultWatchdogConfig()
	if config.Enabled == nil {
		config.Enabled = defaults.Enabled
	}
	if config.PollIntervalSeconds == 0 {
		config.PollIntervalSeconds = defaults.PollIntervalSeconds
	}
	if config.SoftLimitPercent == 0 {
		config.SoftLimitPercent = defaults.SoftLimitPercent
	}
	if config.HardLimitPercent == 0 {
		config.HardLimitPercent = defaults.HardLimitPercent
	}
	if config.GracePeriodSeconds == 0 {
		config.GracePeriodSeconds = defaults.GracePeriodSeconds
	}
	return config
}
