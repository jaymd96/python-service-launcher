package launchlib

import (
	"testing"
)

func TestBuildCommandArgsPEXMode(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModePEX,
		Executable: "service/bin/app.pex",
		Args:       []string{"server", "var/conf/app.yml"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"service/bin/app.pex", "server", "var/conf/app.yml"}
	assertArgs(t, expected, args)
}

func TestBuildCommandArgsPEXWithPython(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModePEX,
		Executable: "service/bin/app.pex",
		PythonPath: "/usr/bin/python3.11",
		PythonOpts: []string{"-O", "-u"},
		Args:       []string{"server"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"/usr/bin/python3.11", "-O", "-u", "service/bin/app.pex", "server"}
	assertArgs(t, expected, args)
}

func TestBuildCommandArgsModuleMode(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModeModule,
		Executable: "myapp.server",
		PythonPath: "/usr/bin/python3",
		Args:       []string{"--port", "8080"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"/usr/bin/python3", "-m", "myapp.server", "--port", "8080"}
	assertArgs(t, expected, args)
}

func TestBuildCommandArgsModuleDefaultPython(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModeModule,
		Executable: "myapp",
	}
	args := BuildCommandArgs(config)
	if args[0] != "python3" {
		t.Errorf("expected python3 as default, got %s", args[0])
	}
	if args[1] != "-m" {
		t.Errorf("expected -m flag, got %s", args[1])
	}
}

func TestBuildCommandArgsScriptMode(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModeScript,
		Executable: "app.py",
		PythonPath: "/usr/bin/python3",
		Args:       []string{"--host", "0.0.0.0"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"/usr/bin/python3", "app.py", "--host", "0.0.0.0"}
	assertArgs(t, expected, args)
}

func TestBuildCommandArgsUvicornMode(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModeUvicorn,
		Executable: "myapp.main",
		EntryPoint: "app",
		PythonPath: "/usr/bin/python3",
		Args:       []string{"--host", "0.0.0.0", "--port", "8000"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"/usr/bin/python3", "-m", "uvicorn", "myapp.main:app", "--host", "0.0.0.0", "--port", "8000"}
	assertArgs(t, expected, args)
}

func TestBuildCommandArgsGunicornMode(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModeGunicorn,
		Executable: "myapp.wsgi",
		EntryPoint: "application",
		PythonPath: "/usr/bin/python3",
		Args:       []string{"-w", "4"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"/usr/bin/python3", "-m", "gunicorn", "myapp.wsgi:application", "-w", "4"}
	assertArgs(t, expected, args)
}

func TestBuildCommandArgsCommandMode(t *testing.T) {
	config := MergedConfig{
		LaunchMode: LaunchModeCommand,
		Executable: "/usr/local/bin/myserver",
		Args:       []string{"--config", "/etc/myserver.yml"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"/usr/local/bin/myserver", "--config", "/etc/myserver.yml"}
	assertArgs(t, expected, args)
}

func TestBuildCommandArgsDefaultMode(t *testing.T) {
	// Empty LaunchMode should default to PEX behavior
	config := MergedConfig{
		Executable: "service/bin/app.pex",
		Args:       []string{"start"},
	}
	args := BuildCommandArgs(config)
	expected := []string{"service/bin/app.pex", "start"}
	assertArgs(t, expected, args)
}

func assertArgs(t *testing.T, expected, actual []string) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v (expected: %v)", len(expected), len(actual), actual, expected)
	}
	for i, a := range expected {
		if actual[i] != a {
			t.Errorf("arg[%d]: expected %q, got %q", i, a, actual[i])
		}
	}
}
