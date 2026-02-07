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

// python-service-launcher is a secure, container-aware launcher for Python PEX binaries
// packaged according to Palantir's Service Layout Specification (SLS).
//
// It replaces shell-based launch scripts (which are vulnerable to environment
// variable injection) with a native Go binary that:
//
//   - Reads declarative YAML configuration (static + per-deployment custom)
//   - Detects cgroup memory limits and configures the Python process accordingly
//   - Runs an RSS watchdog that sends SIGTERM before the OOM killer fires SIGKILL
//   - Manages process lifecycle (PID files, signal forwarding, graceful shutdown)
//   - Sets OS-level resource limits (open files, processes, core dumps)
//   - Tunes glibc malloc to reduce memory fragmentation from C extensions
//
// Usage:
//
//	python-service-launcher                        # launch using default config paths
//	python-service-launcher --startup              # same as above (explicit mode)
//	python-service-launcher --check                # run health check
//	python-service-launcher --status               # check if service is running
//	python-service-launcher --static-config PATH   # override static config path
//	python-service-launcher --custom-config PATH   # override custom config path
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jaymd96/python-service-launcher/launchlib"
)

var (
	// Build-time variables set by -ldflags
	version   = "dev"
	gitCommit = "unknown"
)

func main() {
	// Flags
	staticConfig := flag.String("static-config", "", "Path to static launcher config (default: service/bin/launcher-static.yml)")
	customConfig := flag.String("custom-config", "", "Path to custom launcher config (default: var/conf/launcher-custom.yml)")
	distRootFlag := flag.String("dist-root", "", "Distribution root directory (default: auto-detect from executable path)")
	mode := flag.String("mode", "startup", "Launch mode: startup, check, status")
	checkMode := flag.Bool("check", false, "Run health check instead of starting the service")
	statusMode := flag.Bool("status", false, "Check if the service is running")
	showVersion := flag.Bool("version", false, "Print version and exit")
	serviceName := flag.String("service-name", "", "Service name (auto-detected from config if omitted)")
	serviceVersion := flag.String("service-version", "", "Service version (auto-detected from manifest if omitted)")

	flag.Parse()

	if *showVersion {
		fmt.Printf("python-service-launcher %s (commit: %s)\n", version, gitCommit)
		os.Exit(0)
	}

	// Determine mode from flags
	launchMode := *mode
	if *checkMode {
		launchMode = "check"
	}
	if *statusMode {
		launchMode = "status"
	}

	// Determine distribution root.
	var distRoot string
	if *distRootFlag != "" {
		distRoot = *distRootFlag
	} else {
		// The launcher binary lives at service/bin/<arch>/python-service-launcher,
		// so the dist root is three directories up.
		execPath, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to determine executable path: %v\n", err)
			os.Exit(1)
		}
		distRoot = filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(execPath))))
	}

	// Change to dist root so all relative paths resolve correctly
	if err := os.Chdir(distRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to chdir to distribution root %s: %v\n", distRoot, err)
		os.Exit(1)
	}

	switch launchMode {
	case "startup":
		exitCode := doStartup(*staticConfig, *customConfig, *serviceName, *serviceVersion, distRoot)
		os.Exit(exitCode)

	case "check":
		exitCode := doCheck(*serviceName, distRoot)
		os.Exit(exitCode)

	case "status":
		exitCode := doStatus(*serviceName)
		os.Exit(exitCode)

	default:
		fmt.Fprintf(os.Stderr, "Unknown mode: %s\n", launchMode)
		os.Exit(1)
	}
}

func doStartup(staticConfigPath, customConfigPath, serviceName, serviceVersion, distRoot string) int {
	// Auto-detect service name and version from manifest if not provided
	if serviceName == "" || serviceVersion == "" {
		name, ver, err := readManifestMetadata("deployment/manifest.yml")
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: failed to read manifest: %v\n", err)
			if serviceName == "" {
				serviceName = "unknown"
			}
			if serviceVersion == "" {
				serviceVersion = "0.0.0"
			}
		} else {
			if serviceName == "" {
				serviceName = name
			}
			if serviceVersion == "" {
				serviceVersion = ver
			}
		}
	}

	params := launchlib.LauncherParams{
		DistRoot:         distRoot,
		StaticConfigPath: staticConfigPath,
		CustomConfigPath: customConfigPath,
		ServiceName:      serviceName,
		ServiceVersion:   serviceVersion,
		Stdout:           os.Stdout,
	}

	launcher := launchlib.NewLauncher(params)
	result, err := launcher.Launch()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Launch failed: %v\n", err)
		return 1
	}

	if result.WatchdogTriggered {
		fmt.Fprintf(os.Stderr, "Process was terminated by RSS watchdog (OOM prevention)\n")
	}

	return result.ExitCode
}

func doCheck(serviceName, distRoot string) int {
	// Read the check config and run the health check PEX
	checkConfigPath := "service/bin/launcher-check.yml"
	if _, err := os.Stat(checkConfigPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "No health check configured (missing %s)\n", checkConfigPath)
		return 1
	}

	params := launchlib.LauncherParams{
		DistRoot:         distRoot,
		StaticConfigPath: checkConfigPath,
		ServiceName:      serviceName,
		ServiceVersion:   "check",
		Stdout:           os.Stdout,
	}

	launcher := launchlib.NewLauncher(params)
	result, err := launcher.Launch()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		return 1
	}
	return result.ExitCode
}

func doStatus(serviceName string) int {
	pidPath := fmt.Sprintf("var/run/%s.pid", serviceName)
	pid, err := launchlib.ReadPidFile(pidPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Service not running (no pid file at %s)\n", pidPath)
		return 1
	}

	if !launchlib.IsProcessAlive(pid) {
		fmt.Fprintf(os.Stderr, "Service not running (stale pid file, pid=%d)\n", pid)
		launchlib.RemovePidFile(pidPath)
		return 1
	}

	fmt.Printf("Service running: pid=%d\n", pid)
	return 0
}

// readManifestMetadata extracts product-name and product-version from the SLS manifest.
func readManifestMetadata(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	// Simple line-based parsing to avoid a full YAML dependency in main
	// (the launchlib already uses yaml.v3, but keeping main minimal)
	var name, ver string
	for _, line := range splitLines(string(data)) {
		line = trimSpace(line)
		if hasPrefix(line, "product-name:") {
			name = trimQuotes(trimSpace(line[len("product-name:"):]))
		}
		if hasPrefix(line, "product-version:") {
			ver = trimQuotes(trimSpace(line[len("product-version:"):]))
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("product-name not found in manifest")
	}
	return name, ver, nil
}

// Minimal string helpers to avoid importing strings in main
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func trimQuotes(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}
