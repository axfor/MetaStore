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
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
)

// BenchmarkMaintenance_Status benchmarks the Status RPC
func BenchmarkMaintenance_Status(b *testing.B) {
	b.Run("Memory", func(b *testing.B) {
		benchmarkMaintenanceStatus(b, "memory")
	})
	b.Run("RocksDB", func(b *testing.B) {
		benchmarkMaintenanceStatus(b, "rocksdb")
	})
}

func benchmarkMaintenanceStatus(b *testing.B, storageType string) {
	var clientAddr string
	var cleanup func()

	if storageType == "memory" {
		node, c := startMemoryNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	} else {
		node, c := startRocksDBNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	}
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(clientAddr, grpc.WithInsecure())
	if err != nil {
		b.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(p *testing.PB) {
		for p.Next() {
			_, err := maintenanceClient.Status(ctx, &pb.StatusRequest{})
			if err != nil {
				b.Errorf("Status failed: %v", err)
			}
		}
	})
}

// BenchmarkMaintenance_Hash benchmarks the Hash RPC
func BenchmarkMaintenance_Hash(b *testing.B) {
	b.Run("Memory", func(b *testing.B) {
		benchmarkMaintenanceHash(b, "memory")
	})
	b.Run("RocksDB", func(b *testing.B) {
		benchmarkMaintenanceHash(b, "rocksdb")
	})
}

func benchmarkMaintenanceHash(b *testing.B, storageType string) {
	var clientAddr string
	var cleanup func()

	if storageType == "memory" {
		node, c := startMemoryNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	} else {
		node, c := startRocksDBNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	}
	defer cleanup()

	time.Sleep(2 * time.Second)

	// Put some data first
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("/bench/hash/key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, err = cli.Put(ctx, key, value)
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}
	}

	conn, err := grpc.Dial(clientAddr, grpc.WithInsecure())
	if err != nil {
		b.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := maintenanceClient.Hash(ctx, &pb.HashRequest{})
		if err != nil {
			b.Errorf("Hash failed: %v", err)
		}
	}
}

// BenchmarkMaintenance_HashKV benchmarks the HashKV RPC
func BenchmarkMaintenance_HashKV(b *testing.B) {
	b.Run("Memory", func(b *testing.B) {
		benchmarkMaintenanceHashKV(b, "memory")
	})
	b.Run("RocksDB", func(b *testing.B) {
		benchmarkMaintenanceHashKV(b, "rocksdb")
	})
}

func benchmarkMaintenanceHashKV(b *testing.B, storageType string) {
	var clientAddr string
	var cleanup func()

	if storageType == "memory" {
		node, c := startMemoryNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	} else {
		node, c := startRocksDBNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	}
	defer cleanup()

	time.Sleep(2 * time.Second)

	// Put some data first
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("/bench/hashkv/key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, err = cli.Put(ctx, key, value)
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}
	}

	conn, err := grpc.Dial(clientAddr, grpc.WithInsecure())
	if err != nil {
		b.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := maintenanceClient.HashKV(ctx, &pb.HashKVRequest{Revision: 0})
		if err != nil {
			b.Errorf("HashKV failed: %v", err)
		}
	}
}

// BenchmarkMaintenance_Alarm benchmarks the Alarm RPC
func BenchmarkMaintenance_Alarm(b *testing.B) {
	b.Run("Memory_GET", func(b *testing.B) {
		benchmarkMaintenanceAlarm(b, "memory", pb.AlarmRequest_GET)
	})
	b.Run("Memory_ACTIVATE", func(b *testing.B) {
		benchmarkMaintenanceAlarm(b, "memory", pb.AlarmRequest_ACTIVATE)
	})
	b.Run("RocksDB_GET", func(b *testing.B) {
		benchmarkMaintenanceAlarm(b, "rocksdb", pb.AlarmRequest_GET)
	})
}

func benchmarkMaintenanceAlarm(b *testing.B, storageType string, action pb.AlarmRequest_AlarmAction) {
	var clientAddr string
	var cleanup func()

	if storageType == "memory" {
		node, c := startMemoryNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	} else {
		node, c := startRocksDBNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	}
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(clientAddr, grpc.WithInsecure())
	if err != nil {
		b.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := &pb.AlarmRequest{
			Action: action,
		}
		if action == pb.AlarmRequest_ACTIVATE {
			req.MemberID = uint64(i % 100)
			req.Alarm = pb.AlarmType_NOSPACE
		}
		_, err := maintenanceClient.Alarm(ctx, req)
		if err != nil {
			b.Errorf("Alarm failed: %v", err)
		}
	}
}

// BenchmarkMaintenance_Snapshot benchmarks the Snapshot RPC
func BenchmarkMaintenance_Snapshot(b *testing.B) {
	b.Run("Memory_SmallDB", func(b *testing.B) {
		benchmarkMaintenanceSnapshot(b, "memory", 100)
	})
	b.Run("Memory_MediumDB", func(b *testing.B) {
		benchmarkMaintenanceSnapshot(b, "memory", 1000)
	})
	b.Run("RocksDB_SmallDB", func(b *testing.B) {
		benchmarkMaintenanceSnapshot(b, "rocksdb", 100)
	})
}

func benchmarkMaintenanceSnapshot(b *testing.B, storageType string, numKeys int) {
	var clientAddr string
	var cleanup func()

	if storageType == "memory" {
		node, c := startMemoryNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	} else {
		node, c := startRocksDBNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	}
	defer cleanup()

	time.Sleep(2 * time.Second)

	// Put test data
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("/bench/snapshot/key%d", i)
		value := fmt.Sprintf("value%d_with_some_padding_to_make_it_larger", i)
		_, err = cli.Put(ctx, key, value)
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}
	}

	conn, err := grpc.Dial(clientAddr,
		grpc.WithInsecure(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16*1024*1024)),
	)
	if err != nil {
		b.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stream, err := maintenanceClient.Snapshot(ctx, &pb.SnapshotRequest{})
		if err != nil {
			b.Errorf("Snapshot failed: %v", err)
			continue
		}

		totalSize := 0
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err.Error() != "EOF" {
					b.Errorf("Snapshot recv failed: %v", err)
				}
				break
			}
			totalSize += len(resp.Blob)
		}
	}
}

// BenchmarkMaintenance_Defragment benchmarks the Defragment RPC
func BenchmarkMaintenance_Defragment(b *testing.B) {
	b.Run("Memory", func(b *testing.B) {
		benchmarkMaintenanceDefragment(b, "memory")
	})
	b.Run("RocksDB", func(b *testing.B) {
		benchmarkMaintenanceDefragment(b, "rocksdb")
	})
}

func benchmarkMaintenanceDefragment(b *testing.B, storageType string) {
	var clientAddr string
	var cleanup func()

	if storageType == "memory" {
		node, c := startMemoryNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	} else {
		node, c := startRocksDBNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	}
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(clientAddr, grpc.WithInsecure())
	if err != nil {
		b.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(p *testing.PB) {
		for p.Next() {
			_, err := maintenanceClient.Defragment(ctx, &pb.DefragmentRequest{})
			if err != nil {
				b.Errorf("Defragment failed: %v", err)
			}
		}
	})
}

// BenchmarkMaintenance_MixedWorkload benchmarks mixed Maintenance operations
func BenchmarkMaintenance_MixedWorkload(b *testing.B) {
	b.Run("Memory", func(b *testing.B) {
		benchmarkMaintenanceMixed(b, "memory")
	})
	b.Run("RocksDB", func(b *testing.B) {
		benchmarkMaintenanceMixed(b, "rocksdb")
	})
}

func benchmarkMaintenanceMixed(b *testing.B, storageType string) {
	var clientAddr string
	var cleanup func()

	if storageType == "memory" {
		node, c := startMemoryNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	} else {
		node, c := startRocksDBNode(b, 1)
		clientAddr = node.clientAddr
		cleanup = c
	}
	defer cleanup()

	time.Sleep(2 * time.Second)

	// Put some test data
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		b.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	for i := 0; i < 500; i++ {
		key := fmt.Sprintf("/bench/mixed/key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, err = cli.Put(ctx, key, value)
		if err != nil {
			b.Fatalf("Put failed: %v", err)
		}
	}

	conn, err := grpc.Dial(clientAddr, grpc.WithInsecure())
	if err != nil {
		b.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)

	b.ResetTimer()
	b.RunParallel(func(p *testing.PB) {
		i := 0
		for p.Next() {
			// Mix different operations
			switch i % 5 {
			case 0:
				maintenanceClient.Status(ctx, &pb.StatusRequest{})
			case 1:
				maintenanceClient.Hash(ctx, &pb.HashRequest{})
			case 2:
				maintenanceClient.HashKV(ctx, &pb.HashKVRequest{Revision: 0})
			case 3:
				maintenanceClient.Alarm(ctx, &pb.AlarmRequest{Action: pb.AlarmRequest_GET})
			case 4:
				maintenanceClient.Defragment(ctx, &pb.DefragmentRequest{})
			}
			i++
		}
	})
}
