package launchlib

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerTextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggingConfig{Format: LogFormatText})
	logger.Printf("hello %s", "world")
	output := buf.String()
	if !strings.Contains(output, "hello world") {
		t.Errorf("expected 'hello world' in output, got %q", output)
	}
}

func TestLoggerJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggingConfig{Format: LogFormatJSON})
	logger.Printf("hello %s", "world")
	output := buf.String()
	if !strings.Contains(output, `"message":"hello world"`) {
		t.Errorf("expected JSON message in output, got %q", output)
	}
	if !strings.Contains(output, `"level":"info"`) {
		t.Errorf("expected level:info in output, got %q", output)
	}
}

func TestLoggerJSONWithFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggingConfig{
		Format: LogFormatJSON,
		Fields: map[string]string{"service": "test-svc"},
	})
	logger.Printf("test")
	output := buf.String()
	if !strings.Contains(output, `"service":"test-svc"`) {
		t.Errorf("expected service field in output, got %q", output)
	}
}

func TestLoggerWarnf(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggingConfig{Format: LogFormatText})
	logger.Warnf("something %s", "bad")
	output := buf.String()
	if !strings.Contains(output, "WARNING:") {
		t.Errorf("expected WARNING prefix, got %q", output)
	}
}

func TestLoggerJSONWarn(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggingConfig{Format: LogFormatJSON})
	logger.Warnf("something %s", "bad")
	output := buf.String()
	if !strings.Contains(output, `"level":"warn"`) {
		t.Errorf("expected level:warn in JSON output, got %q", output)
	}
}
