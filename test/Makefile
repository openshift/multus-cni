# Makefile for Multus CNI E2E Test

# Binary name
BINARY_NAME := multus-cni-tests-ext

# Build directory
BUILD_DIR := bin

# Go module and package
GO_MODULE := gopkg.in/k8snetworkplumbingwg/multus-cni.v4

# Version information
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

# Go build flags
LDFLAGS := -X '$(GO_MODULE)/pkg/version.commitFromGit=$(GIT_COMMIT)' \
           -X '$(GO_MODULE)/pkg/version.buildDate=$(BUILD_DATE)' \
           -X '$(GO_MODULE)/pkg/version.versionFromGit=$(GIT_COMMIT)'

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build:
	@echo "Adding vendor files to tests extension"
	go mod vendor
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -v -o $(BUILD_DIR)/$(BINARY_NAME) -mod=vendor -ldflags="$(LDFLAGS)" ./cmd/

# Build for Linux (useful for container builds)
.PHONY: build-linux
build-linux:
	@echo "Adding vendor files to tests extension"
	go mod vendor
	@echo "Building $(BINARY_NAME) for Linux..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -v -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 -mod=vendor -ldflags="$(LDFLAGS)" ./cmd/

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
