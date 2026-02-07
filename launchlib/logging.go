package launchlib

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// LogFormat controls the output format of the launcher's logger.
type LogFormat string

const (
	LogFormatText LogFormat = "text"
	LogFormatJSON LogFormat = "json"
)

// LoggingConfig controls launcher log output.
type LoggingConfig struct {
	// Format selects the log output format. Default: "text".
	Format LogFormat `yaml:"format,omitempty"`

	// Level is the minimum log level. Default: "info".
	Level string `yaml:"level,omitempty"`

	// Fields are extra key-value pairs included in every JSON log line.
	Fields map[string]string `yaml:"fields,omitempty"`
}

// DefaultLoggingConfig returns sensible logging defaults.
func DefaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Format: LogFormatText,
		Level:  "info",
	}
}

// Logger wraps the standard library logger to support structured JSON output.
type Logger struct {
	inner  *log.Logger
	config LoggingConfig
}

// NewLogger creates a Logger based on the configuration.
func NewLogger(w io.Writer, config LoggingConfig) *Logger {
	if w == nil {
		w = os.Stdout
	}
	if config.Format == "" {
		config.Format = LogFormatText
	}
	if config.Level == "" {
		config.Level = "info"
	}
	var inner *log.Logger
	if config.Format == LogFormatJSON {
		inner = log.New(w, "", 0) // no prefix for JSON
	} else {
		inner = log.New(w, "", log.LstdFlags|log.Lmicroseconds)
	}
	return &Logger{inner: inner, config: config}
}

// Printf logs a formatted message.
func (l *Logger) Printf(format string, args ...interface{}) {
	if l.config.Format == LogFormatJSON {
		l.jsonLog("info", fmt.Sprintf(format, args...))
		return
	}
	l.inner.Printf(format, args...)
}

// Println logs a message.
func (l *Logger) Println(msg string) {
	if l.config.Format == LogFormatJSON {
		l.jsonLog("info", msg)
		return
	}
	l.inner.Println(msg)
}

// Warnf logs a warning-level formatted message.
func (l *Logger) Warnf(format string, args ...interface{}) {
	if l.config.Format == LogFormatJSON {
		l.jsonLog("warn", fmt.Sprintf(format, args...))
		return
	}
	l.inner.Printf("WARNING: "+format, args...)
}

// Errorf logs an error-level formatted message.
func (l *Logger) Errorf(format string, args ...interface{}) {
	if l.config.Format == LogFormatJSON {
		l.jsonLog("error", fmt.Sprintf(format, args...))
		return
	}
	l.inner.Printf("ERROR: "+format, args...)
}

func (l *Logger) jsonLog(level, message string) {
	entry := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"level":     level,
		"message":   message,
		"logger":    "python-service-launcher",
	}
	for k, v := range l.config.Fields {
		entry[k] = v
	}
	data, _ := json.Marshal(entry)
	l.inner.Output(0, string(data))
}
