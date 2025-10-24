# MetaStore Makefile
# Build configuration for MetaStore with unified storage engine support

# Binary name
BINARY_NAME=metaStore

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Build flags
LDFLAGS=-ldflags="-s -w"
CGO_LDFLAGS=-lrocksdb -lpthread -lstdc++ -ldl -lm -lzstd -llz4 -lz -lsnappy -lbz2

# Detect OS
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
    # macOS specific settings
    ROCKSDB_PATH ?= /usr/local
else ifeq ($(UNAME_S),Linux)
    # Linux specific settings
    ROCKSDB_PATH ?= /usr/local
else
    # Windows or other
    ROCKSDB_PATH ?= C:/rocksdb
endif

# Colors for output
NO_COLOR=\033[0m
GREEN=\033[0;32m
YELLOW=\033[0;33m
CYAN=\033[0;36m

.PHONY: all build clean test help deps tidy run-memory run-rocksdb cluster-memory cluster-rocksdb install

## all: Default target - build the binary
all: build

## build: Build MetaStore binary with both storage engines
build:
	@echo "$(CYAN)Building MetaStore...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME)
	@echo "$(GREEN)Build complete: $(BINARY_NAME)$(NO_COLOR)"
	@ls -lh $(BINARY_NAME)

## clean: Remove binary and clean build cache
clean:
	@echo "$(YELLOW)Cleaning...$(NO_COLOR)"
	@$(GOCLEAN)
	@rm -f $(BINARY_NAME)
	@rm -rf data/
	@rm -f /tmp/test_*.log
	@echo "$(GREEN)Clean complete$(NO_COLOR)"

## test: Run all tests
test:
	@echo "$(CYAN)Running tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v ./...

## deps: Download dependencies
deps:
	@echo "$(CYAN)Downloading dependencies...$(NO_COLOR)"
	@$(GOGET) -v ./...

## tidy: Tidy and verify dependencies
tidy:
	@echo "$(CYAN)Tidying dependencies...$(NO_COLOR)"
	@$(GOMOD) tidy
	@$(GOMOD) verify

## install: Install the binary to $GOPATH/bin
install: build
	@echo "$(CYAN)Installing $(BINARY_NAME)...$(NO_COLOR)"
	@cp $(BINARY_NAME) $(GOPATH)/bin/
	@echo "$(GREEN)Installed to $(GOPATH)/bin/$(BINARY_NAME)$(NO_COLOR)"

## run-memory: Run single node with memory storage
run-memory: build
	@echo "$(CYAN)Starting MetaStore with memory storage...$(NO_COLOR)"
	@./$(BINARY_NAME) -id 1 -port 9121 -storage memory

## run-rocksdb: Run single node with RocksDB storage
run-rocksdb: build
	@echo "$(CYAN)Starting MetaStore with RocksDB storage...$(NO_COLOR)"
	@mkdir -p data
	@./$(BINARY_NAME) -id 1 -port 9121 -storage rocksdb

## cluster-memory: Start 3-node cluster with memory storage (background)
cluster-memory: build
	@echo "$(CYAN)Starting 3-node cluster with memory storage...$(NO_COLOR)"
	@./$(BINARY_NAME) -id 1 -port 9121 -storage memory -cluster http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023 > /tmp/node1.log 2>&1 &
	@./$(BINARY_NAME) -id 2 -port 9122 -storage memory -cluster http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023 > /tmp/node2.log 2>&1 &
	@./$(BINARY_NAME) -id 3 -port 9123 -storage memory -cluster http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023 > /tmp/node3.log 2>&1 &
	@sleep 2
	@echo "$(GREEN)Cluster started. Check logs: /tmp/node*.log$(NO_COLOR)"
	@echo "$(YELLOW)Stop with: make stop-cluster$(NO_COLOR)"

## cluster-rocksdb: Start 3-node cluster with RocksDB storage (background)
cluster-rocksdb: build
	@echo "$(CYAN)Starting 3-node cluster with RocksDB storage...$(NO_COLOR)"
	@mkdir -p data
	@./$(BINARY_NAME) -id 1 -port 9121 -storage rocksdb -cluster http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023 > /tmp/node1.log 2>&1 &
	@./$(BINARY_NAME) -id 2 -port 9122 -storage rocksdb -cluster http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023 > /tmp/node2.log 2>&1 &
	@./$(BINARY_NAME) -id 3 -port 9123 -storage rocksdb -cluster http://127.0.0.1:9021,http://127.0.0.1:9022,http://127.0.0.1:9023 > /tmp/node3.log 2>&1 &
	@sleep 2
	@echo "$(GREEN)Cluster started. Check logs: /tmp/node*.log$(NO_COLOR)"
	@echo "$(YELLOW)Stop with: make stop-cluster$(NO_COLOR)"

## stop-cluster: Stop all running MetaStore processes
stop-cluster:
	@echo "$(YELLOW)Stopping all MetaStore processes...$(NO_COLOR)"
	@pkill -f $(BINARY_NAME) || true
	@sleep 1
	@echo "$(GREEN)All processes stopped$(NO_COLOR)"

## status: Show cluster status
status:
	@echo "$(CYAN)Checking cluster status...$(NO_COLOR)"
	@ps aux | grep $(BINARY_NAME) | grep -v grep || echo "No processes running"
	@echo ""
	@echo "$(CYAN)Data directory:$(NO_COLOR)"
	@ls -la data/ 2>/dev/null || echo "No data directory"

## help: Show this help message
help:
	@echo "$(CYAN)MetaStore Makefile Commands:$(NO_COLOR)"
	@echo ""
	@sed -n 's/^##//p' Makefile | column -t -s ':' | sed -e 's/^/  /'
	@echo ""
	@echo "$(YELLOW)Examples:$(NO_COLOR)"
	@echo "  make build              # Build the binary"
	@echo "  make run-memory         # Run with memory storage"
	@echo "  make cluster-rocksdb    # Start 3-node RocksDB cluster"
	@echo "  make stop-cluster       # Stop all nodes"
	@echo "  make clean              # Clean build artifacts"
