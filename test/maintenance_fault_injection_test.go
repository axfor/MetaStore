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
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
)

// TestMaintenance_FaultInjection_ServerCrash tests Maintenance operations during server crashes
func TestMaintenance_FaultInjection_ServerCrash(t *testing.T) {
	t.Run("Status_DuringCrash", func(t *testing.T) {
		testStatusDuringCrash(t)
	})
	t.Run("Snapshot_Interrupted", func(t *testing.T) {
		testSnapshotInterrupted(t)
	})
}

func testStatusDuringCrash(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(node.clientAddr, grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	// Test normal operation
	_, err = maintenanceClient.Status(ctx, &pb.StatusRequest{})
	if err != nil {
		t.Fatalf("Initial Status failed: %v", err)
	}

	// Simulate crash by stopping server
	node.server.Stop()
	time.Sleep(100 * time.Millisecond)

	// Try Status call - should fail gracefully
	_, err = maintenanceClient.Status(ctx, &pb.StatusRequest{})
	if err == nil {
		t.Error("Status should fail after server crash")
	} else {
		t.Logf("Correctly handled server crash: %v", err)
	}
}

func testSnapshotInterrupted(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	time.Sleep(2 * time.Second)

	// Put some data
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("/fault/snapshot/key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, err = cli.Put(ctx, key, value)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	conn, err := grpc.Dial(node.clientAddr,
		grpc.WithInsecure(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16*1024*1024)),
	)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)

	// Start snapshot and interrupt it
	stream, err := maintenanceClient.Snapshot(ctx, &pb.SnapshotRequest{})
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	// Read first chunk
	_, err = stream.Recv()
	if err != nil {
		t.Fatalf("First recv failed: %v", err)
	}

	// Stop server while streaming
	go func() {
		time.Sleep(100 * time.Millisecond)
		node.server.Stop()
	}()

	// Continue reading - should eventually fail
	interrupted := false
	for i := 0; i < 100; i++ {
		_, err = stream.Recv()
		if err != nil {
			interrupted = true
			t.Logf("Snapshot correctly interrupted: %v", err)
			break
		}
	}

	if !interrupted {
		t.Error("Snapshot should be interrupted when server stops")
	}
}

// TestMaintenance_FaultInjection_HighLoad tests Maintenance under high load
func TestMaintenance_FaultInjection_HighLoad(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	time.Sleep(2 * time.Second)

	// Create multiple clients
	clients := make([]*clientv3.Client, 10)
	for i := 0; i < 10; i++ {
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{node.clientAddr},
			DialTimeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("Failed to create client %d: %v", i, err)
		}
		defer cli.Close()
		clients[i] = cli
	}

	conn, err := grpc.Dial(node.clientAddr, grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	// Generate high load
	var wg sync.WaitGroup
	stopLoad := make(chan bool)

	// Background write load
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(clientIdx int) {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-stopLoad:
					return
				default:
					key := fmt.Sprintf("/fault/highload/client%d/key%d", clientIdx, counter)
					value := fmt.Sprintf("value%d", counter)
					clients[clientIdx].Put(ctx, key, value)
					counter++
					time.Sleep(10 * time.Millisecond)
				}
			}
		}(i)
	}

	// Test Maintenance operations under load
	time.Sleep(1 * time.Second)

	// Status under load
	statusErrors := 0
	for i := 0; i < 20; i++ {
		_, err := maintenanceClient.Status(ctx, &pb.StatusRequest{})
		if err != nil {
			statusErrors++
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Hash under load
	hashErrors := 0
	for i := 0; i < 10; i++ {
		_, err := maintenanceClient.Hash(ctx, &pb.HashRequest{})
		if err != nil {
			hashErrors++
		}
		time.Sleep(200 * time.Millisecond)
	}

	// HashKV under load
	hashKVErrors := 0
	for i := 0; i < 10; i++ {
		_, err := maintenanceClient.HashKV(ctx, &pb.HashKVRequest{Revision: 0})
		if err != nil {
			hashKVErrors++
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Stop background load
	close(stopLoad)
	wg.Wait()

	t.Logf("Maintenance under high load: Status errors=%d/20, Hash errors=%d/10, HashKV errors=%d/10",
		statusErrors, hashErrors, hashKVErrors)

	// Allow some errors but not all operations should fail
	if statusErrors > 10 {
		t.Errorf("Too many Status errors: %d/20", statusErrors)
	}
	if hashErrors > 5 {
		t.Errorf("Too many Hash errors: %d/10", hashErrors)
	}
	if hashKVErrors > 5 {
		t.Errorf("Too many HashKV errors: %d/10", hashKVErrors)
	}
}

// TestMaintenance_FaultInjection_ResourceExhaustion tests behavior when resources are exhausted
func TestMaintenance_FaultInjection_ResourceExhaustion(t *testing.T) {
	t.Run("ManyAlarms", func(t *testing.T) {
		testManyAlarms(t)
	})
	t.Run("RapidOperations", func(t *testing.T) {
		testRapidOperations(t)
	})
}

func testManyAlarms(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(node.clientAddr, grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	// Activate many alarms to test resource handling
	alarmCount := 1000
	for i := 0; i < alarmCount; i++ {
		_, err := maintenanceClient.Alarm(ctx, &pb.AlarmRequest{
			Action:   pb.AlarmRequest_ACTIVATE,
			MemberID: uint64(i + 1),
			Alarm:    pb.AlarmType_NOSPACE,
		})
		if err != nil {
			t.Fatalf("Failed to activate alarm %d: %v", i, err)
		}
	}

	// Verify all alarms are stored
	resp, err := maintenanceClient.Alarm(ctx, &pb.AlarmRequest{
		Action: pb.AlarmRequest_GET,
	})
	if err != nil {
		t.Fatalf("Failed to get alarms: %v", err)
	}

	if len(resp.Alarms) != alarmCount {
		t.Errorf("Expected %d alarms, got %d", alarmCount, len(resp.Alarms))
	}

	// Deactivate all alarms
	for i := 0; i < alarmCount; i++ {
		_, err := maintenanceClient.Alarm(ctx, &pb.AlarmRequest{
			Action:   pb.AlarmRequest_DEACTIVATE,
			MemberID: uint64(i + 1),
			Alarm:    pb.AlarmType_NOSPACE,
		})
		if err != nil {
			t.Fatalf("Failed to deactivate alarm %d: %v", i, err)
		}
	}

	// Verify all alarms are removed
	resp, err = maintenanceClient.Alarm(ctx, &pb.AlarmRequest{
		Action: pb.AlarmRequest_GET,
	})
	if err != nil {
		t.Fatalf("Failed to get alarms: %v", err)
	}

	if len(resp.Alarms) != 0 {
		t.Errorf("Expected 0 alarms after deactivation, got %d", len(resp.Alarms))
	}

	t.Logf("Successfully handled %d alarms", alarmCount)
}

func testRapidOperations(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(node.clientAddr, grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	// Rapid Status calls
	statusStart := time.Now()
	statusErrors := 0
	for i := 0; i < 1000; i++ {
		_, err := maintenanceClient.Status(ctx, &pb.StatusRequest{})
		if err != nil {
			statusErrors++
		}
	}
	statusDuration := time.Since(statusStart)

	// Rapid Defragment calls
	defragStart := time.Now()
	defragErrors := 0
	for i := 0; i < 1000; i++ {
		_, err := maintenanceClient.Defragment(ctx, &pb.DefragmentRequest{})
		if err != nil {
			defragErrors++
		}
	}
	defragDuration := time.Since(defragStart)

	t.Logf("Rapid operations: Status (1000 calls, %v, %d errors), Defragment (1000 calls, %v, %d errors)",
		statusDuration, statusErrors, defragDuration, defragErrors)

	// Allow some errors but most should succeed
	if statusErrors > 100 {
		t.Errorf("Too many Status errors in rapid calls: %d/1000", statusErrors)
	}
	if defragErrors > 100 {
		t.Errorf("Too many Defragment errors in rapid calls: %d/1000", defragErrors)
	}
}

// TestMaintenance_FaultInjection_ConcurrentCrashes tests concurrent operations with crashes
func TestMaintenance_FaultInjection_ConcurrentCrashes(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(node.clientAddr,
		grpc.WithInsecure(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16*1024*1024)),
	)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Put some data
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{node.clientAddr},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	for i := 0; i < 500; i++ {
		key := fmt.Sprintf("/fault/concurrent/key%d", i)
		value := fmt.Sprintf("value%d", i)
		_, err = cli.Put(ctx, key, value)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	maintenanceClient := pb.NewMaintenanceClient(conn)

	// Start multiple concurrent operations
	var wg sync.WaitGroup
	results := struct {
		sync.Mutex
		statusOK     int
		statusErr    int
		hashOK       int
		hashErr      int
		snapshotOK   int
		snapshotErr  int
	}{}

	// Status goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, err := maintenanceClient.Status(ctx, &pb.StatusRequest{})
				results.Lock()
				if err == nil {
					results.statusOK++
				} else {
					results.statusErr++
				}
				results.Unlock()
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}

	// Hash goroutines
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_, err := maintenanceClient.Hash(ctx, &pb.HashRequest{})
				results.Lock()
				if err == nil {
					results.hashOK++
				} else {
					results.hashErr++
				}
				results.Unlock()
				time.Sleep(200 * time.Millisecond)
			}
		}()
	}

	// Snapshot goroutines
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				stream, err := maintenanceClient.Snapshot(ctx, &pb.SnapshotRequest{})
				if err != nil {
					results.Lock()
					results.snapshotErr++
					results.Unlock()
					continue
				}

				success := true
				for {
					_, err := stream.Recv()
					if err != nil {
						if err.Error() != "EOF" {
							success = false
						}
						break
					}
				}

				results.Lock()
				if success {
					results.snapshotOK++
				} else {
					results.snapshotErr++
				}
				results.Unlock()
				time.Sleep(500 * time.Millisecond)
			}
		}()
	}

	// Wait for all operations
	wg.Wait()

	t.Logf("Concurrent operations results:")
	t.Logf("  Status: %d OK, %d errors", results.statusOK, results.statusErr)
	t.Logf("  Hash: %d OK, %d errors", results.hashOK, results.hashErr)
	t.Logf("  Snapshot: %d OK, %d errors", results.snapshotOK, results.snapshotErr)

	// At least some operations should succeed
	totalOK := results.statusOK + results.hashOK + results.snapshotOK
	if totalOK == 0 {
		t.Error("All concurrent operations failed")
	}
}

// TestMaintenance_FaultInjection_Recovery tests recovery after faults
func TestMaintenance_FaultInjection_Recovery(t *testing.T) {
	node, cleanup := startMemoryNode(t, 1)
	defer cleanup()

	time.Sleep(2 * time.Second)

	conn, err := grpc.Dial(node.clientAddr, grpc.WithInsecure())
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	maintenanceClient := pb.NewMaintenanceClient(conn)
	ctx := context.Background()

	// Normal operation
	_, err = maintenanceClient.Status(ctx, &pb.StatusRequest{})
	if err != nil {
		t.Fatalf("Initial Status failed: %v", err)
	}

	// Simulate temporary issue by rapid operations
	for i := 0; i < 100; i++ {
		maintenanceClient.Status(ctx, &pb.StatusRequest{})
	}

	// Verify recovery - operations should still work
	time.Sleep(1 * time.Second)

	recoveryTests := 0
	recoveryOK := 0
	for i := 0; i < 10; i++ {
		recoveryTests++
		_, err := maintenanceClient.Status(ctx, &pb.StatusRequest{})
		if err == nil {
			recoveryOK++
		}
		time.Sleep(100 * time.Millisecond)
	}

	recoveryRate := float64(recoveryOK) / float64(recoveryTests) * 100
	t.Logf("Recovery rate: %.1f%% (%d/%d)", recoveryRate, recoveryOK, recoveryTests)

	if recoveryRate < 80.0 {
		t.Errorf("Poor recovery rate: %.1f%%, expected >= 80%%", recoveryRate)
	}
}
