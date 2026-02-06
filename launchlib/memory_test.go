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
	"testing"
	"testing/fstest"
)

// testFS creates a fake filesystem for testing cgroup scenarios.
func testFS(files map[string]string) fs.FS {
	m := fstest.MapFS{}
	for path, content := range files {
		m[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

func TestDetectCgroupV2(t *testing.T) {
	filesystem := testFS(map[string]string{
		"sys/fs/cgroup/cgroup.controllers": "cpu memory io",
		"sys/fs/cgroup/memory.max":         "1073741824",
	})

	limiter := NewMemoryLimiterWithFS(filesystem)
	version, err := limiter.detectCgroupVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Errorf("expected cgroup v2, got v%d", version)
	}
}

func TestDetectCgroupV1(t *testing.T) {
	filesystem := testFS(map[string]string{
		"sys/fs/cgroup/memory/memory.limit_in_bytes": "2147483648",
	})

	limiter := NewMemoryLimiterWithFS(filesystem)
	version, err := limiter.detectCgroupVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Errorf("expected cgroup v1, got v%d", version)
	}
}

func TestReadCgroupV2MemoryLimit(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected uint64
		wantErr  bool
	}{
		{
			name:     "1 GiB limit",
			content:  "1073741824\n",
			expected: 1073741824,
		},
		{
			name:     "512 MiB limit",
			content:  "536870912\n",
			expected: 536870912,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filesystem := testFS(map[string]string{
				"sys/fs/cgroup/cgroup.controllers": "cpu memory io",
				"sys/fs/cgroup/memory.max":         tt.content,
			})

			limiter := NewMemoryLimiterWithFS(filesystem)
			limit, err := limiter.readCgroupMemoryLimit(2)
			if (err != nil) != tt.wantErr {
				t.Fatalf("unexpected error: %v", err)
			}
			if limit != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, limit)
			}
		})
	}
}

func TestReadCgroupV2Unlimited(t *testing.T) {
	// When cgroup v2 reports "max", we fall back to total system memory
	filesystem := testFS(map[string]string{
		"sys/fs/cgroup/cgroup.controllers": "cpu memory io",
		"sys/fs/cgroup/memory.max":         "max\n",
		"proc/meminfo":                     "MemTotal:       16384000 kB\nMemFree:         8192000 kB\n",
	})

	limiter := NewMemoryLimiterWithFS(filesystem)
	limit, err := limiter.readCgroupMemoryLimit(2)
	if err != nil {
		t.Fatal(err)
	}
	// 16384000 kB = 16777216000 bytes
	expected := uint64(16384000 * 1024)
	if limit != expected {
		t.Errorf("expected %d, got %d", expected, limit)
	}
}

func TestReadCgroupV1Unlimited(t *testing.T) {
	// cgroup v1 uses a very large number for unlimited
	filesystem := testFS(map[string]string{
		"sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
		"proc/meminfo": "MemTotal:       8192000 kB\nMemFree:         4096000 kB\n",
	})

	limiter := NewMemoryLimiterWithFS(filesystem)
	limit, err := limiter.readCgroupMemoryLimit(1)
	if err != nil {
		t.Fatal(err)
	}
	expected := uint64(8192000 * 1024)
	if limit != expected {
		t.Errorf("expected %d (system memory), got %d", expected, limit)
	}
}

func TestComputeLimitsCgroupAware(t *testing.T) {
	filesystem := testFS(map[string]string{
		"sys/fs/cgroup/cgroup.controllers": "cpu memory io",
		"sys/fs/cgroup/memory.max":         "1073741824", // 1 GiB
	})

	limiter := NewMemoryLimiterWithFS(filesystem)

	config := MergedConfig{
		IsContainer: true,
		Memory: MemoryConfig{
			Mode:                    MemoryModeCgroupAware,
			MaxRSSPercent:           75,
			HeapFragmentationBuffer: 0.10,
		},
		Watchdog: WatchdogConfig{
			SoftLimitPercent: 85,
			HardLimitPercent: 95,
		},
	}

	limits, err := limiter.ComputeLimits(config)
	if err != nil {
		t.Fatal(err)
	}

	if limits.CgroupLimitBytes != 1073741824 {
		t.Errorf("expected cgroup limit 1073741824, got %d", limits.CgroupLimitBytes)
	}

	// effective = 1073741824 * 0.75 * 0.90 = 724566425
	expectedEffective := uint64(float64(1073741824) * 0.75 * 0.90)
	if limits.EffectiveLimitBytes != expectedEffective {
		t.Errorf("expected effective limit %d, got %d", expectedEffective, limits.EffectiveLimitBytes)
	}

	// soft warn = 1073741824 * 0.85 = 912680550
	expectedSoftWarn := uint64(float64(1073741824) * 0.85)
	if limits.SoftWarnBytes != expectedSoftWarn {
		t.Errorf("expected soft warn %d, got %d", expectedSoftWarn, limits.SoftWarnBytes)
	}

	// hard kill = 1073741824 * 0.95 = 1020054732
	expectedHardKill := uint64(float64(1073741824) * 0.95)
	if limits.HardKillBytes != expectedHardKill {
		t.Errorf("expected hard kill %d, got %d", expectedHardKill, limits.HardKillBytes)
	}

	if limits.CgroupVersion != 2 {
		t.Errorf("expected cgroup v2, got v%d", limits.CgroupVersion)
	}
}

func TestComputeLimitsFixed(t *testing.T) {
	limiter := NewMemoryLimiterWithFS(testFS(map[string]string{}))

	config := MergedConfig{
		Memory: MemoryConfig{
			Mode:                    MemoryModeFixed,
			FixedLimitBytes:         512 * 1024 * 1024, // 512 MiB
			MaxRSSPercent:           75,
			HeapFragmentationBuffer: 0.10,
		},
		Watchdog: WatchdogConfig{
			SoftLimitPercent: 85,
			HardLimitPercent: 95,
		},
	}

	limits, err := limiter.ComputeLimits(config)
	if err != nil {
		t.Fatal(err)
	}

	if limits.CgroupLimitBytes != 512*1024*1024 {
		t.Errorf("expected fixed limit 512 MiB, got %d", limits.CgroupLimitBytes)
	}
}

func TestComputeLimitsUnmanaged(t *testing.T) {
	limiter := NewMemoryLimiterWithFS(testFS(map[string]string{}))

	config := MergedConfig{
		Memory: MemoryConfig{
			Mode: MemoryModeUnmanaged,
		},
	}

	limits, err := limiter.ComputeLimits(config)
	if err != nil {
		t.Fatal(err)
	}

	if limits.EffectiveLimitBytes != 0 {
		t.Errorf("expected 0 effective limit for unmanaged, got %d", limits.EffectiveLimitBytes)
	}
}

func TestComputeLimitsMinimumFloor(t *testing.T) {
	// Test that very small cgroup limits don't go below the minimum
	filesystem := testFS(map[string]string{
		"sys/fs/cgroup/cgroup.controllers": "cpu memory io",
		"sys/fs/cgroup/memory.max":         "33554432", // 32 MiB
	})

	limiter := NewMemoryLimiterWithFS(filesystem)

	config := MergedConfig{
		IsContainer: true,
		Memory: MemoryConfig{
			Mode:                    MemoryModeCgroupAware,
			MaxRSSPercent:           75,
			HeapFragmentationBuffer: 0.10,
		},
		Watchdog: WatchdogConfig{
			SoftLimitPercent: 85,
			HardLimitPercent: 95,
		},
	}

	limits, err := limiter.ComputeLimits(config)
	if err != nil {
		t.Fatal(err)
	}

	// 32 MiB * 0.75 * 0.90 = ~21.6 MiB, which is below the 64 MiB floor
	if limits.EffectiveLimitBytes < minimumEffectiveLimitBytes {
		t.Errorf("effective limit %d should not be below minimum %d",
			limits.EffectiveLimitBytes, minimumEffectiveLimitBytes)
	}
}

func TestBuildMemoryEnv(t *testing.T) {
	config := MergedConfig{
		Memory: MemoryConfig{
			Mode:                MemoryModeCgroupAware,
			MallocArenaMax:      2,
			MallocTrimThreshold: 131072,
		},
	}
	limits := MemoryLimits{
		CgroupLimitBytes:    1073741824,
		EffectiveLimitBytes: 724566425,
	}

	env := BuildMemoryEnv(config, limits)

	if env["SLS_MEMORY_LIMIT_BYTES"] != fmt.Sprintf("%d", limits.EffectiveLimitBytes) {
		t.Errorf("unexpected SLS_MEMORY_LIMIT_BYTES: %s", env["SLS_MEMORY_LIMIT_BYTES"])
	}
	if env["SLS_CGROUP_LIMIT_BYTES"] != fmt.Sprintf("%d", limits.CgroupLimitBytes) {
		t.Errorf("unexpected SLS_CGROUP_LIMIT_BYTES: %s", env["SLS_CGROUP_LIMIT_BYTES"])
	}
	if env["MALLOC_ARENA_MAX"] != "2" {
		t.Errorf("unexpected MALLOC_ARENA_MAX: %s", env["MALLOC_ARENA_MAX"])
	}
	if env["PYTHONMALLOC"] != "malloc" {
		t.Errorf("expected PYTHONMALLOC=malloc, got %s", env["PYTHONMALLOC"])
	}
}

func TestBuildMemoryEnvUnmanaged(t *testing.T) {
	config := MergedConfig{
		Memory: MemoryConfig{
			Mode: MemoryModeUnmanaged,
		},
	}
	limits := MemoryLimits{}

	env := BuildMemoryEnv(config, limits)

	if len(env) != 0 {
		t.Errorf("expected no env vars for unmanaged mode, got %v", env)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    uint64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KiB"},
		{1048576, "1.00 MiB"},
		{1073741824, "1.00 GiB"},
		{536870912, "512.00 MiB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatBytes(tt.bytes)
			if result != tt.expected {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, result, tt.expected)
			}
		})
	}
}
