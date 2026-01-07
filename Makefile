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
CGO_LDFLAGS_BASE=-lpthread -lstdc++ -ldl -lm -lz -lbz2

# Detect OS and Architecture
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

ifeq ($(UNAME_S),Darwin)
    # macOS specific settings
    OS_ARCH := darwin-$(UNAME_M)
    # macOS doesn't support full static linking
    CGO_LDFLAGS_BASE += -Wl,-U,_SecTrustCopyCertificateChain
else ifeq ($(UNAME_S),Linux)
    # Linux specific settings
    OS_ARCH := linux-$(UNAME_M)
else
    # Windows or other
    OS_ARCH := windows-$(UNAME_M)
endif

# Use bundled RocksDB from third_party (static libraries)
ROCKSDB_DIR := $(shell pwd)/third_party/rocksdb/$(OS_ARCH)
CGO_CFLAGS = -I$(ROCKSDB_DIR)/include
# Add library path so -lrocksdb can find our static libraries
# Put both -L flag and explicit .a files to ensure static linking
CGO_LDFLAGS = -L$(ROCKSDB_DIR)/lib $(ROCKSDB_DIR)/lib/librocksdb.a $(ROCKSDB_DIR)/lib/libzstd.a $(ROCKSDB_DIR)/lib/liblz4.a $(ROCKSDB_DIR)/lib/libsnappy.a $(CGO_LDFLAGS_BASE)

# Colors for output
NO_COLOR=\033[0m
GREEN=\033[0;32m
YELLOW=\033[0;33m
CYAN=\033[0;36m

.PHONY: all build clean test help deps tidy run-memory run-rocksdb cluster-memory cluster-rocksdb install test-perf test-perf-memory test-perf-rocksdb benchmark rocksdb-download rocksdb rocksdb-clean rocksdb-compression

## all: Default target - build the binary
all: build

## build: Build MetaStore binary with both storage engines (using GreenTea GC)
build: rocksdb
	@echo "$(CYAN)Building MetaStore with GreenTea GC...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) $(CMD_PATH)
	@echo "$(GREEN)Build complete: $(BINARY_NAME)$(NO_COLOR)"
	@ls -lh $(BINARY_NAME)

## clean: Remove binary and clean build cache
clean:
	@echo "$(YELLOW)Cleaning...$(NO_COLOR)"
	@$(GOCLEAN)
	@rm -f $(BINARY_NAME)
	@rm -rf data/
	@rm -rf test/data/
	@rm -rf /tmp/metastore-test-*
	@rm -f /tmp/test_*.log
	@rm -rf third_party/rocksdb/darwin-arm64/*
	@echo "$(GREEN)Clean complete$(NO_COLOR)"

## test: Run all tests (excluding performance/benchmark tests)
test:
	@echo "$(CYAN)Running all tests with GreenTea GC (excluding performance/benchmark tests)...$(NO_COLOR)"
	@echo "$(YELLOW)Cleaning test data directories...$(NO_COLOR)"
	@rm -rf data/
	@rm -rf test/data/
	@rm -rf /tmp/metastore-test-*
	@echo "$(YELLOW)Testing pkg packages...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=5m ./pkg/...
	@echo "$(YELLOW)Testing internal packages...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=30m ./internal/...
	@echo "$(YELLOW)Testing integration and system tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=30m -skip "Performance|Benchmark" ./test/
	@echo "$(GREEN)All tests passed!$(NO_COLOR)"

## test-unit: Run only unit tests (no integration tests)
test-unit:
	@echo "$(CYAN)Running unit tests...$(NO_COLOR)"
	@$(GOTEST) -v -timeout=5m ./pkg/...
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GOTEST) -v -timeout=15m ./internal/...

## test-integration: Run only integration tests
test-integration:
	@echo "$(CYAN)Running integration tests...$(NO_COLOR)"
	@echo "$(YELLOW)Cleaning test data directories...$(NO_COLOR)"
	@rm -rf data/
	@rm -rf test/data/
	@rm -rf /tmp/metastore-test-*
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

## benchmark: Run benchmark tests
benchmark:
	@echo "$(CYAN)Running benchmark tests...$(NO_COLOR)"
	@CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" GOEXPERIMENT=greenteagc $(GOTEST) -v -timeout=30m -run="Benchmark" ./test/
	@echo "$(GREEN)Benchmark tests completed!$(NO_COLOR)"

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
	@echo "  make test               # Run all tests (excluding perf/benchmark)"
	@echo "  make test-unit          # Run unit tests only"
	@echo "  make test-integration   # Run integration tests only"
	@echo "  make test-perf          # Run all performance tests"
	@echo "  make benchmark          # Run benchmark tests"
	@echo "  make test-perf-memory   # Run Memory performance tests only"
	@echo "  make test-perf-rocksdb  # Run RocksDB performance tests only"
	@echo "  make run-memory         # Run with memory storage"
	@echo "  make cluster-rocksdb    # Start 3-node RocksDB cluster"
	@echo "  make stop-cluster       # Stop all nodes"
	@echo "  make rocksdb-download   # Download RocksDB source code"
	@echo "  make rocksdb            # Build RocksDB from local source"
	@echo "  make clean              # Clean build artifacts"

## rocksdb-download: Download RocksDB source code
rocksdb-download:
	@echo "$(CYAN)Downloading RocksDB 10.4.2 source code...$(NO_COLOR)"
	@if [ -d "third_party/rocksdb/src/v10.4.2" ]; then \
		echo "$(YELLOW)RocksDB source already exists at third_party/rocksdb/src/v10.4.2$(NO_COLOR)"; \
		echo "$(YELLOW)To re-download, first remove: rm -rf third_party/rocksdb/src/v10.4.2$(NO_COLOR)"; \
	else \
		mkdir -p third_party/rocksdb/src; \
		git clone --depth 1 --branch v10.4.2 https://github.com/facebook/rocksdb.git third_party/rocksdb/src/v10.4.2; \
		echo "$(GREEN)RocksDB source downloaded to: third_party/rocksdb/src/v10.4.2$(NO_COLOR)"; \
	fi

## rocksdb: Build RocksDB and dependencies from local source
rocksdb: rocksdb-clean
	@# Check if libraries already exist
	@if [ -f "$(ROCKSDB_DIR)/lib/librocksdb.a" ] && \
	   [ -f "$(ROCKSDB_DIR)/lib/libzstd.a" ] && \
	   [ -f "$(ROCKSDB_DIR)/lib/liblz4.a" ] && \
	   [ -f "$(ROCKSDB_DIR)/lib/libsnappy.a" ]; then \
		echo "$(GREEN)RocksDB libraries already built for $(OS_ARCH)$(NO_COLOR)"; \
		exit 0; \
	fi
	@echo "$(CYAN)Building RocksDB from source for $(OS_ARCH)...$(NO_COLOR)"
	@echo "$(YELLOW)This will take 10-20 minutes...$(NO_COLOR)"
	@# Check if source exists
	@if [ ! -d "third_party/rocksdb/src/v10.4.2" ]; then \
		echo "$(YELLOW)RocksDB source not found!$(NO_COLOR)"; \
		echo "$(YELLOW)Please run 'make rocksdb-download' first to download the source code$(NO_COLOR)"; \
		exit 1; \
	fi
	@mkdir -p $(ROCKSDB_DIR)/include
	@mkdir -p $(ROCKSDB_DIR)/lib
	@# Build RocksDB static library
	@echo "$(YELLOW)Compiling RocksDB (this may take a while)...$(NO_COLOR)"
	@cd third_party/rocksdb/src/v10.4.2 && \
		EXTRA_CXXFLAGS="-Wno-error=unused-parameter -Wno-unused-parameter" \
		$(MAKE) static_lib -j$$(sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo 4)
	@# Copy headers
	@echo "$(YELLOW)Installing headers...$(NO_COLOR)"
	@cp -r third_party/rocksdb/src/v10.4.2/include/rocksdb $(ROCKSDB_DIR)/include/
	@# Copy static library
	@echo "$(YELLOW)Installing static library...$(NO_COLOR)"
	@cp third_party/rocksdb/src/v10.4.2/librocksdb.a $(ROCKSDB_DIR)/lib/
	@# Build compression libraries
	@$(MAKE) rocksdb-compression
	@echo "$(GREEN)RocksDB dependencies built successfully!$(NO_COLOR)"
	@echo "$(GREEN)Libraries installed to: $(ROCKSDB_DIR)/lib$(NO_COLOR)"
	@ls -lh $(ROCKSDB_DIR)/lib/

## rocksdb-compression: Build compression libraries from source
rocksdb-compression:
	@echo "$(YELLOW)Building compression libraries...$(NO_COLOR)"
ifeq ($(UNAME_S),Darwin)
	@# On macOS, use Homebrew to get compression libraries
	@echo "$(YELLOW)Copying compression libraries from Homebrew...$(NO_COLOR)"
	@if command -v brew >/dev/null 2>&1; then \
		for lib in lz4 zstd snappy; do \
			if brew list $$lib >/dev/null 2>&1; then \
				echo "Copying $$lib..."; \
				cp $$(brew --prefix $$lib)/lib/lib$$lib.a $(ROCKSDB_DIR)/lib/ 2>/dev/null || true; \
			else \
				echo "$(YELLOW)$$lib not installed, installing via Homebrew...$(NO_COLOR)"; \
				brew install $$lib; \
				cp $$(brew --prefix $$lib)/lib/lib$$lib.a $(ROCKSDB_DIR)/lib/; \
			fi \
		done \
	else \
		echo "$(YELLOW)Homebrew not found, skipping compression libraries$(NO_COLOR)"; \
	fi
else
	@# On Linux, build from source or use system libraries
	@echo "$(YELLOW)Looking for system compression libraries...$(NO_COLOR)"
	@for lib in lz4 zstd snappy; do \
		if [ -f "/usr/lib/x86_64-linux-gnu/lib$$lib.a" ]; then \
			cp /usr/lib/x86_64-linux-gnu/lib$$lib.a $(ROCKSDB_DIR)/lib/; \
		elif [ -f "/usr/lib64/lib$$lib.a" ]; then \
			cp /usr/lib64/lib$$lib.a $(ROCKSDB_DIR)/lib/; \
		elif [ -f "/usr/lib/lib$$lib.a" ]; then \
			cp /usr/lib/lib$$lib.a $(ROCKSDB_DIR)/lib/; \
		else \
			echo "$(YELLOW)$$lib not found in standard paths$(NO_COLOR)"; \
		fi \
	done
endif

## rocksdb-clean: Clean RocksDB build artifacts
rocksdb-clean: 
	@rm -rf third_party/rocksdb/darwin-arm64/*
	@echo "$(YELLOW)Cleaning RocksDB build artifacts...$(NO_COLOR)"
	@if [ -d "third_party/rocksdb/src/v10.4.2" ]; then \
		cd third_party/rocksdb/src/v10.4.2 && $(MAKE) clean; \
	fi
	@echo "$(GREEN)Clean complete$(NO_COLOR)"

