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
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadStaticConfig(t *testing.T) {
	configYAML := `
configType: python
configVersion: 1
executable: service/bin/my-service.pex
entryPoint: my_service.server:main
args:
  - server
  - var/conf/my-service.yml
env:
  PYTHONUNBUFFERED: "1"
  CUSTOM_VAR: hello
pythonOpts:
  - "-O"
memory:
  mode: cgroup-aware
  maxRssPercent: 80
  heapFragmentationBuffer: 0.15
  mallocArenaMax: 4
watchdog:
  pollIntervalSeconds: 10
  softLimitPercent: 80
  hardLimitPercent: 90
  gracePeriodSeconds: 60
resources:
  maxOpenFiles: 32768
dirs:
  - var/data/tmp
  - var/log
  - var/run
`
	dir := t.TempDir()
	path := filepath.Join(dir, "launcher-static.yml")
	if err := os.WriteFile(path, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	config, err := readStaticConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if config.ConfigType != "python" {
		t.Errorf("expected configType python, got %s", config.ConfigType)
	}
	if config.ConfigVersion != 1 {
		t.Errorf("expected configVersion 1, got %d", config.ConfigVersion)
	}
	if config.Executable != "service/bin/my-service.pex" {
		t.Errorf("unexpected executable: %s", config.Executable)
	}
	if config.EntryPoint != "my_service.server:main" {
		t.Errorf("unexpected entryPoint: %s", config.EntryPoint)
	}
	if len(config.Args) != 2 {
		t.Errorf("expected 2 args, got %d", len(config.Args))
	}
	if config.Env["CUSTOM_VAR"] != "hello" {
		t.Errorf("unexpected env: %v", config.Env)
	}
	if config.Memory.MaxRSSPercent != 80 {
		t.Errorf("expected maxRssPercent 80, got %f", config.Memory.MaxRSSPercent)
	}
	if config.Memory.HeapFragmentationBuffer != 0.15 {
		t.Errorf("expected heapFragmentationBuffer 0.15, got %f", config.Memory.HeapFragmentationBuffer)
	}
	if config.Watchdog.PollIntervalSeconds != 10 {
		t.Errorf("expected watchdog poll 10, got %d", config.Watchdog.PollIntervalSeconds)
	}
	if config.Resources.MaxOpenFiles != 32768 {
		t.Errorf("expected maxOpenFiles 32768, got %d", config.Resources.MaxOpenFiles)
	}
}

func TestReadCustomConfig(t *testing.T) {
	configYAML := `
configType: python
configVersion: 1
env:
  EXTRA_VAR: custom_value
  PYTHONUNBUFFERED: "0"
args:
  - --debug
memory:
  maxRssPercent: 60
`
	dir := t.TempDir()
	path := filepath.Join(dir, "launcher-custom.yml")
	if err := os.WriteFile(path, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	config, err := readCustomConfig(path, &buf)
	if err != nil {
		t.Fatal(err)
	}

	if config.Env["EXTRA_VAR"] != "custom_value" {
		t.Errorf("unexpected env: %v", config.Env)
	}
	if len(config.Args) != 1 || config.Args[0] != "--debug" {
		t.Errorf("unexpected args: %v", config.Args)
	}
	if config.Memory.MaxRSSPercent != 60 {
		t.Errorf("expected maxRssPercent 60, got %f", config.Memory.MaxRSSPercent)
	}
}

func TestReadCustomConfigMissing(t *testing.T) {
	var buf bytes.Buffer
	config, err := readCustomConfig("/nonexistent/path.yml", &buf)
	if err != nil {
		t.Fatal("missing custom config should not be an error")
	}
	if config.Env != nil {
		t.Errorf("expected nil env for missing custom config, got %v", config.Env)
	}
	if !bytes.Contains(buf.Bytes(), []byte("not found")) {
		t.Errorf("expected 'not found' message in output")
	}
}

func TestValidateStaticConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  StaticLauncherConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: StaticLauncherConfig{
				ConfigType:    "python",
				ConfigVersion: 1,
				Executable:    "service/bin/app.pex",
			},
			wantErr: false,
		},
		{
			name: "wrong config type",
			config: StaticLauncherConfig{
				ConfigType:    "java",
				ConfigVersion: 1,
				Executable:    "service/bin/app.pex",
			},
			wantErr: true,
		},
		{
			name: "wrong config version",
			config: StaticLauncherConfig{
				ConfigType:    "python",
				ConfigVersion: 2,
				Executable:    "service/bin/app.pex",
			},
			wantErr: true,
		},
		{
			name: "empty executable",
			config: StaticLauncherConfig{
				ConfigType:    "python",
				ConfigVersion: 1,
				Executable:    "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStaticConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStaticConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMergeConfigs(t *testing.T) {
	static := StaticLauncherConfig{
		ConfigType:    "python",
		ConfigVersion: 1,
		Executable:    "service/bin/app.pex",
		Args:          []string{"server", "var/conf/app.yml"},
		Env: map[string]string{
			"BASE_VAR": "from_static",
			"SHARED":   "static_value",
		},
		PythonOpts: []string{"-O"},
		Memory: MemoryConfig{
			Mode:          MemoryModeCgroupAware,
			MaxRSSPercent: 75,
		},
	}

	custom := CustomLauncherConfig{
		Env: map[string]string{
			"CUSTOM_VAR": "from_custom",
			"SHARED":     "custom_value", // should override static
		},
		Args:       []string{"--debug"},
		PythonOpts: []string{"-u"},
	}

	merged := MergeConfigs(static, custom)

	// Args should be concatenated
	if len(merged.Args) != 3 {
		t.Errorf("expected 3 args, got %d: %v", len(merged.Args), merged.Args)
	}
	if merged.Args[2] != "--debug" {
		t.Errorf("expected --debug as last arg, got %s", merged.Args[2])
	}

	// Env should be merged with custom overriding static
	if merged.Env["BASE_VAR"] != "from_static" {
		t.Errorf("expected BASE_VAR=from_static, got %s", merged.Env["BASE_VAR"])
	}
	if merged.Env["CUSTOM_VAR"] != "from_custom" {
		t.Errorf("expected CUSTOM_VAR=from_custom, got %s", merged.Env["CUSTOM_VAR"])
	}
	if merged.Env["SHARED"] != "custom_value" {
		t.Errorf("expected SHARED=custom_value (custom overrides static), got %s", merged.Env["SHARED"])
	}

	// PythonOpts should be concatenated
	if len(merged.PythonOpts) != 2 {
		t.Errorf("expected 2 python opts, got %d: %v", len(merged.PythonOpts), merged.PythonOpts)
	}
}

func TestBuildCommandArgs(t *testing.T) {
	t.Run("pex direct execution", func(t *testing.T) {
		config := MergedConfig{
			Executable: "service/bin/app.pex",
			Args:       []string{"server", "var/conf/app.yml"},
		}
		args := BuildCommandArgs(config)
		expected := []string{"service/bin/app.pex", "server", "var/conf/app.yml"}
		if len(args) != len(expected) {
			t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
		}
		for i, a := range expected {
			if args[i] != a {
				t.Errorf("arg[%d]: expected %q, got %q", i, a, args[i])
			}
		}
	})

	t.Run("explicit python path", func(t *testing.T) {
		config := MergedConfig{
			Executable: "service/bin/app.pex",
			PythonPath: "/usr/bin/python3.11",
			PythonOpts: []string{"-O", "-u"},
			Args:       []string{"server"},
		}
		args := BuildCommandArgs(config)
		expected := []string{"/usr/bin/python3.11", "-O", "-u", "service/bin/app.pex", "server"}
		if len(args) != len(expected) {
			t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
		}
		for i, a := range expected {
			if args[i] != a {
				t.Errorf("arg[%d]: expected %q, got %q", i, a, args[i])
			}
		}
	})
}

func TestDefaultsApplied(t *testing.T) {
	static := StaticLauncherConfig{
		ConfigType:    "python",
		ConfigVersion: 1,
		Executable:    "service/bin/app.pex",
	}
	custom := CustomLauncherConfig{}
	merged := MergeConfigs(static, custom)

	// Memory defaults
	if merged.Memory.Mode != MemoryModeCgroupAware {
		t.Errorf("expected default memory mode cgroup-aware, got %s", merged.Memory.Mode)
	}
	if merged.Memory.MaxRSSPercent != 75 {
		t.Errorf("expected default maxRssPercent 75, got %f", merged.Memory.MaxRSSPercent)
	}
	if merged.Memory.HeapFragmentationBuffer != 0.10 {
		t.Errorf("expected default heapFragmentationBuffer 0.10, got %f", merged.Memory.HeapFragmentationBuffer)
	}
	if merged.Memory.MallocArenaMax != 2 {
		t.Errorf("expected default mallocArenaMax 2, got %d", merged.Memory.MallocArenaMax)
	}

	// Watchdog defaults
	if merged.Watchdog.Enabled == nil || !*merged.Watchdog.Enabled {
		t.Error("expected watchdog enabled by default")
	}
	if merged.Watchdog.PollIntervalSeconds != 5 {
		t.Errorf("expected default poll interval 5, got %d", merged.Watchdog.PollIntervalSeconds)
	}
	if merged.Watchdog.SoftLimitPercent != 85 {
		t.Errorf("expected default soft limit 85, got %f", merged.Watchdog.SoftLimitPercent)
	}
	if merged.Watchdog.HardLimitPercent != 95 {
		t.Errorf("expected default hard limit 95, got %f", merged.Watchdog.HardLimitPercent)
	}
}
