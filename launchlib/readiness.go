package launchlib

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// ReadinessConfig controls the readiness probe.
type ReadinessConfig struct {
	// Enabled controls whether the readiness probe runs. Default: false.
	Enabled bool `yaml:"enabled,omitempty"`

	// HTTPPort is the port for the readiness HTTP endpoint. Default: 8081.
	HTTPPort int `yaml:"httpPort,omitempty"`

	// HTTPPath is the path for the readiness endpoint. Default: "/ready".
	HTTPPath string `yaml:"httpPath,omitempty"`

	// DrainSeconds is how long to report not-ready after receiving SIGTERM.
	// This allows load balancers to drain connections before the process exits.
	// Default: 10.
	DrainSeconds int `yaml:"drainSeconds,omitempty"`

	// FilePath, if set, creates a file when ready and removes it during drain.
	FilePath string `yaml:"filePath,omitempty"`
}

// DefaultReadinessConfig returns sensible readiness defaults.
func DefaultReadinessConfig() ReadinessConfig {
	return ReadinessConfig{
		HTTPPort:     8081,
		HTTPPath:     "/ready",
		DrainSeconds: 10,
	}
}

// ReadinessProbe manages the readiness state of the service.
type ReadinessProbe struct {
	config ReadinessConfig
	logger *Logger
	ready  atomic.Bool
	server *http.Server
}

// NewReadinessProbe creates a new readiness probe.
func NewReadinessProbe(config ReadinessConfig, logger *Logger) *ReadinessProbe {
	if config.HTTPPort == 0 {
		config.HTTPPort = 8081
	}
	if config.HTTPPath == "" {
		config.HTTPPath = "/ready"
	}
	if config.DrainSeconds == 0 {
		config.DrainSeconds = 10
	}
	return &ReadinessProbe{
		config: config,
		logger: logger,
	}
}

// Start begins serving the readiness endpoint and marks the service as ready.
func (p *ReadinessProbe) Start(ctx context.Context) {
	if !p.config.Enabled {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc(p.config.HTTPPath, func(w http.ResponseWriter, r *http.Request) {
		if p.ready.Load() {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "OK")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "NOT READY")
		}
	})

	p.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", p.config.HTTPPort),
		Handler: mux,
	}

	go func() {
		p.logger.Printf("Readiness probe listening on :%d%s", p.config.HTTPPort, p.config.HTTPPath)
		if err := p.server.ListenAndServe(); err != http.ErrServerClosed {
			p.logger.Errorf("Readiness probe failed: %v", err)
		}
	}()

	// Wait for context cancellation to shut down
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.server.Shutdown(shutdownCtx)
	}()
}

// SetReady marks the service as ready.
func (p *ReadinessProbe) SetReady() {
	p.ready.Store(true)
	if p.config.FilePath != "" {
		if err := os.WriteFile(p.config.FilePath, []byte("ready\n"), 0644); err != nil {
			p.logger.Warnf("Failed to write readiness file %s: %v", p.config.FilePath, err)
		}
	}
	p.logger.Printf("Service marked as ready")
}

// Drain marks the service as not ready and waits for the drain period.
func (p *ReadinessProbe) Drain() {
	if !p.config.Enabled {
		return
	}
	p.ready.Store(false)
	if p.config.FilePath != "" {
		_ = os.Remove(p.config.FilePath)
	}
	drainDuration := time.Duration(p.config.DrainSeconds) * time.Second
	p.logger.Printf("Draining for %s before shutdown", drainDuration)
	time.Sleep(drainDuration)
}
