# GABS Build Configuration
# This Makefile demonstrates how to build GABS with version information

# Version information
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Build flags
LDFLAGS = -ldflags "\
	-X github.com/pardeike/gabs/internal/version.Version=$(VERSION) \
	-X github.com/pardeike/gabs/internal/version.Commit=$(COMMIT) \
	-X github.com/pardeike/gabs/internal/version.BuildDate=$(BUILD_DATE)"

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build:
	go build $(LDFLAGS) -o gabs ./cmd/gabs

# Build with debug information
.PHONY: build-debug
build-debug:
	go build -gcflags="all=-N -l" $(LDFLAGS) -o gabs ./cmd/gabs

# Run tests
.PHONY: test
test:
	go test -v ./...

# Clean build artifacts
.PHONY: clean
clean:
	rm -f gabs gabs-test

# Install the binary
.PHONY: install
install:
	go install $(LDFLAGS) ./cmd/gabs

# Show version information that would be embedded
.PHONY: version-info
version-info:
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build Date: $(BUILD_DATE)"

# Build for multiple platforms (example)
.PHONY: build-all
build-all:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o gabs-linux-amd64 ./cmd/gabs
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o gabs-darwin-arm64 ./cmd/gabs
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o gabs-windows-amd64.exe ./cmd/gabs

.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build        - Build the GABS binary with version information"
	@echo "  build-debug  - Build with debug symbols"
	@echo "  test         - Run all tests"
	@echo "  clean        - Remove build artifacts"
	@echo "  install      - Install the binary"
	@echo "  version-info - Show version information that would be embedded"
	@echo "  build-all    - Build for multiple platforms"
	@echo "  help         - Show this help message"