// Copyright 2025 The axfor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// BenchmarkPut benchmarks PUT operations
func BenchmarkPut(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/put-%d", i)
		_, err := cli.Put(ctx, key, "benchmark-value")
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}
	}
}

// BenchmarkGet benchmarks GET operations
func BenchmarkGet(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Pre-populate keys
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("/bench/get-%d", i)
		_, err := cli.Put(ctx, key, "benchmark-value")
		if err != nil {
			b.Fatalf("Setup failed: %v", err)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/get-%d", i%1000)
		_, err := cli.Get(ctx, key)
		if err != nil {
			b.Fatalf("Get failed: %v", err)
		}
	}
}

// BenchmarkDelete benchmarks DELETE operations
func BenchmarkDelete(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Pre-populate keys for deletion
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/del-%d", i)
		_, err := cli.Put(ctx, key, "benchmark-value")
		if err != nil {
			b.Fatalf("Setup failed: %v", err)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/del-%d", i)
		_, err := cli.Delete(ctx, key)
		if err != nil {
			b.Fatalf("Delete failed: %v", err)
		}
	}
}

// BenchmarkRange benchmarks RANGE operations
func BenchmarkRange(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Pre-populate keys
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("/bench/range/%03d", i)
		_, err := cli.Put(ctx, key, "benchmark-value")
		if err != nil {
			b.Fatalf("Setup failed: %v", err)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := cli.Get(ctx, "/bench/range/", clientv3.WithPrefix())
		if err != nil {
			b.Fatalf("Range failed: %v", err)
		}
	}
}

// BenchmarkTransaction benchmarks transaction operations
func BenchmarkTransaction(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/txn-%d", i)
		txn := cli.Txn(ctx).
			If(clientv3.Compare(clientv3.Version(key), "=", 0)).
			Then(clientv3.OpPut(key, "new-value")).
			Else(clientv3.OpGet(key))

		_, err := txn.Commit()
		if err != nil {
			b.Fatalf("Transaction failed: %v", err)
		}
	}
}

// BenchmarkPutParallel benchmarks parallel PUT operations
func BenchmarkPutParallel(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("/bench/parallel-%d", i)
			_, err := cli.Put(ctx, key, "parallel-value")
			if err != nil {
				b.Fatalf("Put failed: %v", err)
			}
			i++
		}
	})
}

// BenchmarkGetParallel benchmarks parallel GET operations
func BenchmarkGetParallel(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Pre-populate keys
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("/bench/parallel-get-%d", i)
		_, err := cli.Put(ctx, key, "value")
		if err != nil {
			b.Fatalf("Setup failed: %v", err)
		}
	}

	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("/bench/parallel-get-%d", i%1000)
			_, err := cli.Get(ctx, key)
			if err != nil {
				b.Fatalf("Get failed: %v", err)
			}
			i++
		}
	})
}

// BenchmarkWatch benchmarks watch creation and event delivery
func BenchmarkWatch(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/watch-%d", i)

		// Create watch
		watchChan := cli.Watch(ctx, key)

		// Trigger event
		_, err := cli.Put(ctx, key, "value")
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}

		// Wait for event
		select {
		case <-watchChan:
			// Event received
		case <-time.After(1 * time.Second):
			b.Fatal("Watch timeout")
		}
	}
}

// BenchmarkLease benchmarks lease operations
func BenchmarkLease(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Grant lease
		lease, err := cli.Grant(ctx, 60)
		if err != nil {
			b.Fatalf("Grant failed: %v", err)
		}

		// Put with lease
		key := fmt.Sprintf("/bench/lease-%d", i)
		_, err = cli.Put(ctx, key, "value", clientv3.WithLease(lease.ID))
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}

		// Revoke lease
		_, err = cli.Revoke(ctx, lease.ID)
		if err != nil {
			b.Fatalf("Revoke failed: %v", err)
		}
	}
}

// BenchmarkSmallValue benchmarks operations with small values (100B)
func BenchmarkSmallValue(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	value := string(make([]byte, 100)) // 100 bytes
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/small-%d", i)
		_, err := cli.Put(ctx, key, value)
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}
	}
}

// BenchmarkLargeValue benchmarks operations with large values (1MB)
func BenchmarkLargeValue(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	value := string(make([]byte, 1024*1024)) // 1MB
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("/bench/large-%d", i)
		_, err := cli.Put(ctx, key, value)
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}
	}
}

// BenchmarkMixedOperations benchmarks realistic mixed workload
func BenchmarkMixedOperations(b *testing.B) {
	node, cleanup := startMemoryNode(b, 1)
	defer cleanup()

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Pre-populate some keys
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("/bench/mixed-%d", i)
		_, _ = cli.Put(ctx, key, "initial-value")
	}

	b.ResetTimer()

	// Mixed workload: 50% GET, 30% PUT, 10% DELETE, 10% RANGE
	for i := 0; i < b.N; i++ {
		op := i % 10
		key := fmt.Sprintf("/bench/mixed-%d", i%100)

		switch {
		case op < 5: // 50% GET
			_, err := cli.Get(ctx, key)
			if err != nil {
				b.Fatalf("Get failed: %v", err)
			}
		case op < 8: // 30% PUT
			_, err := cli.Put(ctx, key, "updated-value")
			if err != nil {
				b.Fatalf("Put failed: %v", err)
			}
		case op < 9: // 10% DELETE
			_, _ = cli.Delete(ctx, key)
		default: // 10% RANGE
			_, _ = cli.Get(ctx, "/bench/mixed-", clientv3.WithPrefix(), clientv3.WithLimit(10))
		}
	}
}
