VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.gitCommit=$(GIT_COMMIT)

BINARY_NAME := python-service-launcher
CMD_PATH := ./cmd/python-service-launcher

# Target platforms matching SLS distribution expectations
PLATFORMS := \
	darwin-amd64 \
	darwin-arm64 \
	linux-amd64 \
	linux-arm64

.PHONY: all build test clean dist fmt vet lint

all: test build

# Build for the current platform
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) $(CMD_PATH)

# Cross-compile for all SLS target platforms
dist: $(PLATFORMS)

darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o dist/$@/$(BINARY_NAME) $(CMD_PATH)

darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o dist/$@/$(BINARY_NAME) $(CMD_PATH)

linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" \
		-o dist/$@/$(BINARY_NAME) $(CMD_PATH)

linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" \
		-o dist/$@/$(BINARY_NAME) $(CMD_PATH)

test:
	go test -v -race -count=1 ./...

test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint: vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipping"; \
	fi

clean:
	rm -rf bin/ dist/ coverage.out coverage.html

# Package the init.sh alongside binaries for inclusion in SLS dists
package: dist
	cp scripts/init.sh dist/init.sh
	chmod +x dist/init.sh
	@echo "Packaging complete. Contents:"
	@find dist -type f | sort

# Install locally for development
install: build
	cp bin/$(BINARY_NAME) $(GOPATH)/bin/ 2>/dev/null || \
		cp bin/$(BINARY_NAME) $(HOME)/go/bin/

.DEFAULT_GOAL := all
