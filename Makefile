# MetaStore Makefile
# Build configuration for MetaStore with unified storage engine support

# Binary name
BINARY_NAME=metaStore
CMD_PATH=./cmd/metastore

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
    # macOS specific settings - add Wl,-U for missing Security framework symbols
    CGO_LDFLAGS += -Wl,-U,_SecTrustCopyCertificateChain
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

.PHONY: all build clean test help deps tidy run-memory run-rocksdb cluster-memory cluster-rocksdb install test-perf test-perf-memory test-perf-rocksdb

## all: Default target - build the binary
all: build

## build: Build MetaStore binary with both storage engines (using GreenTea GC)
build:
	@echo "$(CYAN)Building MetaStore with GreenTea GC...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) $(CMD_PATH)
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

## test: Run all tests (including RocksDB storage tests)
test:
	@echo "$(CYAN)Running all tests with GreenTea GC...$(NO_COLOR)"
	@echo "$(YELLOW)Testing pkg packages...$(NO_COLOR)"
	@$(GOTEST) -v -timeout=5m ./pkg/...
	@echo "$(YELLOW)Testing internal packages...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=30m ./internal/...
	@echo "$(YELLOW)Testing integration and system tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=120m ./test/
	@echo "$(GREEN)All tests passed!$(NO_COLOR)"

## test-unit: Run only unit tests (no integration tests)
test-unit:
	@echo "$(CYAN)Running unit tests...$(NO_COLOR)"
	@$(GOTEST) -v -timeout=5m ./pkg/...
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=15m ./internal/...

## test-integration: Run only integration tests
test-integration:
	@echo "$(CYAN)Running integration tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=20m ./test/

## test-storage: Run only RocksDB storage tests
test-storage:
	@echo "$(CYAN)Running RocksDB storage tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v ./internal/rocksdb/

## test-coverage: Run tests with coverage report
test-coverage:
	@echo "$(CYAN)Running tests with coverage...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -timeout=20m -coverprofile=coverage.out ./internal/... ./test/
	@go tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)Coverage report generated: coverage.html$(NO_COLOR)"

## test-maintenance: Run only Maintenance Service tests
test-maintenance:
	@echo "$(CYAN)Running Maintenance Service tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestMaintenance_" ./test/
	@echo "$(GREEN)Maintenance tests passed!$(NO_COLOR)"

## test-quick: Run quick tests (Maintenance only, for rapid verification)
test-quick:
	@echo "$(CYAN)Running quick tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=10m -run="TestMaintenance_(Status|Hash|Alarm)" ./test/
	@echo "$(GREEN)Quick tests passed!$(NO_COLOR)"

## test-perf-memory: Run Memory storage performance tests
test-perf-memory:
	@echo "$(CYAN)Running Memory storage performance tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=20m -run="TestMemoryPerformance_" ./test/
	@echo "$(GREEN)Memory performance tests completed!$(NO_COLOR)"

## test-perf-rocksdb: Run RocksDB storage performance tests
test-perf-rocksdb:
	@echo "$(CYAN)Running RocksDB storage performance tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=20m -run="TestRocksDBPerformance_" ./test/
	@echo "$(GREEN)RocksDB performance tests completed!$(NO_COLOR)"

## test-perf: Run all performance tests (Memory + RocksDB)
test-perf:
	@echo "$(CYAN)Running all performance tests...$(NO_COLOR)"
	@echo "$(YELLOW)Testing Memory storage performance...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=20m -run="TestMemoryPerformance_" ./test/
	@echo "$(YELLOW)Testing RocksDB storage performance...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=20m -run="TestRocksDBPerformance_" ./test/
	@echo "$(GREEN)All performance tests completed!$(NO_COLOR)"

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
	@echo "  make test               # Run all tests"
	@echo "  make test-unit          # Run unit tests only"
	@echo "  make test-integration   # Run integration tests only"
	@echo "  make test-perf          # Run all performance tests"
	@echo "  make test-perf-memory   # Run Memory performance tests only"
	@echo "  make test-perf-rocksdb  # Run RocksDB performance tests only"
	@echo "  make run-memory         # Run with memory storage"
	@echo "  make cluster-rocksdb    # Start 3-node RocksDB cluster"
	@echo "  make stop-cluster       # Stop all nodes"
	@echo "  make clean              # Clean build artifacts"
