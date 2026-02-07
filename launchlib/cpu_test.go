package launchlib

import (
	"runtime"
	"testing"
)

func TestDetectCPUCountOverride(t *testing.T) {
	config := CPUConfig{Override: 4}
	count := DetectCPUCount(config, testFS(map[string]string{}))
	if count != 4 {
		t.Errorf("expected 4 from override, got %d", count)
	}
}

func TestDetectCPUCountAutoDetectFallback(t *testing.T) {
	config := CPUConfig{AutoDetect: true}
	// No cgroup files => falls back to runtime.NumCPU()
	count := DetectCPUCount(config, testFS(map[string]string{}))
	if count != runtime.NumCPU() {
		t.Errorf("expected runtime.NumCPU() = %d, got %d", runtime.NumCPU(), count)
	}
}

func TestDetectCPUCountDisabled(t *testing.T) {
	config := CPUConfig{AutoDetect: false}
	count := DetectCPUCount(config, testFS(map[string]string{}))
	if count != runtime.NumCPU() {
		t.Errorf("expected runtime.NumCPU() = %d, got %d", runtime.NumCPU(), count)
	}
}

func TestDetectCPUCountCgroupV2(t *testing.T) {
	// 200000/100000 = 2 CPUs
	fs := testFS(map[string]string{
		"sys/fs/cgroup/cpu.max": "200000 100000\n",
	})
	config := CPUConfig{AutoDetect: true}
	count := DetectCPUCount(config, fs)
	if count != 2 {
		t.Errorf("expected 2 CPUs from cgroup v2, got %d", count)
	}
}

func TestDetectCPUCountCgroupV2Unlimited(t *testing.T) {
	fs := testFS(map[string]string{
		"sys/fs/cgroup/cpu.max": "max 100000\n",
	})
	config := CPUConfig{AutoDetect: true}
	count := DetectCPUCount(config, fs)
	if count != runtime.NumCPU() {
		t.Errorf("expected runtime.NumCPU() for unlimited, got %d", count)
	}
}

func TestDetectCPUCountCgroupV1(t *testing.T) {
	fs := testFS(map[string]string{
		"sys/fs/cgroup/cpu/cpu.cfs_quota_us":  "300000\n",
		"sys/fs/cgroup/cpu/cpu.cfs_period_us": "100000\n",
	})
	config := CPUConfig{AutoDetect: true}
	count := DetectCPUCount(config, fs)
	if count != 3 {
		t.Errorf("expected 3 CPUs from cgroup v1, got %d", count)
	}
}

func TestDetectCPUCountCgroupV1Unlimited(t *testing.T) {
	fs := testFS(map[string]string{
		"sys/fs/cgroup/cpu/cpu.cfs_quota_us":  "-1\n",
		"sys/fs/cgroup/cpu/cpu.cfs_period_us": "100000\n",
	})
	config := CPUConfig{AutoDetect: true}
	count := DetectCPUCount(config, fs)
	if count != runtime.NumCPU() {
		t.Errorf("expected runtime.NumCPU() for unlimited, got %d", count)
	}
}

func TestBuildCPUEnv(t *testing.T) {
	env := BuildCPUEnv(4)
	if env["OMP_NUM_THREADS"] != "4" {
		t.Errorf("expected OMP_NUM_THREADS=4, got %s", env["OMP_NUM_THREADS"])
	}
	if env["SERVICE_CPU_COUNT"] != "4" {
		t.Errorf("expected SERVICE_CPU_COUNT=4, got %s", env["SERVICE_CPU_COUNT"])
	}
}
