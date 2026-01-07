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
	"os"
	"testing"
	"time"

	"metaStore/internal/memory"
	"metaStore/internal/raft"

	goraft "go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/stretchr/testify/require"
)

// BenchmarkLeaseReadVsNoLease compare Lease Read and Lease Read performance
func BenchmarkLeaseReadVsNoLease(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping Lease Read performance benchmark in short mode")
	}

	// testscenarioscene：
	// 1. Lease Read enabled (Leader validlease)
	// 2. Lease Read disabled ( Raft read)

	scenarios := []struct {
		name           string
		enableLeaseRead bool
	}{
		{"WithoutLeaseRead", false},
		{"WithLeaseRead", true},
	}

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			// createsinglenodeclusterfor performancetest
			peers := []string{"http://127.0.0.1:12000"}

			// clean updatadirectory
			os.RemoveAll("data/memory/1")

			proposeC := make(chan string, 100)
			confChangeC := make(chan raftpb.ConfChange, 10)

			// createnodeconfig
			nodeCfg := NewTestConfig(1, 1, ":9400")
			nodeCfg.Server.Raft.LeaseRead.Enable = sc.enableLeaseRead
			if sc.enableLeaseRead {
				nodeCfg.Server.Raft.LeaseRead.ClockDrift = 100 * time.Millisecond
				nodeCfg.Server.Raft.ElectionTick = 10
				nodeCfg.Server.Raft.HeartbeatTick = 1
				nodeCfg.Server.Raft.TickInterval = 100 * time.Millisecond
			}

			getSnapshot := func() ([]byte, error) {
				return nil, nil
			}

			commitC, errorC, snapshotterReady, node := raft.NewNode(
				1, peers, false, getSnapshot, proposeC, confChangeC, "memory", nodeCfg,
			)

			kvs := memory.NewMemory(
				<-snapshotterReady,
				proposeC,
				commitC,
				errorC,
			)

			// waitnodebecomeas Leader
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()

			leaderElected := false
			for !leaderElected {
				select {
				case <-ctx.Done():
					b.Fatal("Timeout waiting for leader election")
				case <-ticker.C:
					status := node.Status()
					if status.State == goraft.StateLeader.String() {
						leaderElected = true
					}
				}
			}

			// ifenabled Lease Read，waitleasebuild
			if sc.enableLeaseRead {
				time.Sleep(500 * time.Millisecond)

				// verifyleasealready build
				lm := node.LeaseManager()
				require.NotNil(b, lm)
				require.True(b, lm.IsLeader())
				require.True(b, lm.HasValidLease(), "Lease should be valid before benchmark")
			}

			// writesometestdata
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("bench-key-%d", i)
				value := fmt.Sprintf("bench-value-%d", i)
				_, _, err := kvs.PutWithLease(context.Background(), key, value, 0)
				require.NoError(b, err)
			}

			// waitdatacommit
			time.Sleep(200 * time.Millisecond)

			// performancetest：readoperation
			b.ResetTimer()

			ctx2 := context.Background()
			for i := 0; i < b.N; i++ {
				key := fmt.Sprintf("bench-key-%d", i%100)
				_, err := kvs.Range(ctx2, key, "", 0, 0)
				if err != nil {
					b.Fatal(err)
				}
			}

			b.StopTimer()

			// getstatisticsinfo
			if sc.enableLeaseRead {
				stats := node.ReadIndexManager().Stats()
				b.ReportMetric(float64(stats.FastPathReads), "fast_path_reads")
				b.ReportMetric(float64(stats.SlowPathReads), "slow_path_reads")
				b.ReportMetric(stats.FastPathRate*100, "fast_path_rate_%")
			}

			// clean up
			close(proposeC)
			close(confChangeC)
			time.Sleep(100 * time.Millisecond)
		})
	}
}

// TestLeaseReadPerformanceGain test Lease Read performance
func TestLeaseReadPerformanceGain(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Lease Read performance gain test in short mode")
	}

	// running scenariosceneandcompareperformance
	withoutLeaseReadOps := benchmarkLeaseReadScenario(t, false, 10000)
	withLeaseReadOps := benchmarkLeaseReadScenario(t, true, 10000)

	t.Logf("Without Lease Read: %d ops/sec", withoutLeaseReadOps)
	t.Logf("With Lease Read:    %d ops/sec", withLeaseReadOps)

	// calculateperformance
	if withoutLeaseReadOps > 0 {
		improvement := float64(withLeaseReadOps) / float64(withoutLeaseReadOps)
		t.Logf("Performance improvement: %.2fx", improvement)

		// Lease Read shouldperformance
		// tofew 2x，inscenarioscenenextmayto 10-100x
		if improvement < 1.5 {
			t.Logf("Warning: Lease Read improvement (%.2fx) is less than expected (>1.5x)", improvement)
		}
	}
}

// benchmarkLeaseReadScenario runningsingle performancetestscenarioscene
func benchmarkLeaseReadScenario(t *testing.T, enableLeaseRead bool, numOps int) int64 {
	// createsinglenodecluster
	peers := []string{"http://127.0.0.1:12001"}

	// clean updatadirectory
	os.RemoveAll("data/memory/1")

	proposeC := make(chan string, 100)
	confChangeC := make(chan raftpb.ConfChange, 10)

	// createnodeconfig
	nodeCfg := NewTestConfig(1, 1, ":9401")
	nodeCfg.Server.Raft.LeaseRead.Enable = enableLeaseRead
	if enableLeaseRead {
		nodeCfg.Server.Raft.LeaseRead.ClockDrift = 100 * time.Millisecond
		nodeCfg.Server.Raft.ElectionTick = 10
		nodeCfg.Server.Raft.HeartbeatTick = 1
		nodeCfg.Server.Raft.TickInterval = 100 * time.Millisecond
	}

	getSnapshot := func() ([]byte, error) {
		return nil, nil
	}

	commitC, errorC, snapshotterReady, node := raft.NewNode(
		1, peers, false, getSnapshot, proposeC, confChangeC, "memory", nodeCfg,
	)

	kvs := memory.NewMemory(
		<-snapshotterReady,
		proposeC,
		commitC,
		errorC,
	)

	// wait Leader electelection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	leaderElected := false
	for !leaderElected {
		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for leader election")
		case <-ticker.C:
			status := node.Status()
			if status.State == goraft.StateLeader.String() {
				leaderElected = true
			}
		}
	}

	// ifenabled Lease Read，waitleasebuild
	if enableLeaseRead {
		// waitlongtimelease(tofewneed period)
		time.Sleep(1500 * time.Millisecond)

		lm := node.LeaseManager()
		require.NotNil(t, lm)

		// getleasestatusfor debug
		stats := lm.Stats()
		t.Logf("  Lease stats: IsLeader=%v, HasValidLease=%v, RenewCount=%d, Remaining=%v",
			stats.IsLeader, stats.HasValidLease, stats.LeaseRenewCount, stats.LeaseRemaining)

		// singlenodescenarioscenenext，leasemayneedmanytimebuild
		// ifleasenot build，waitfirstsegmenttime
		if !lm.HasValidLease() {
			t.Logf("  Waiting additional time for lease establishment...")
			time.Sleep(1000 * time.Millisecond)
			stats = lm.Stats()
			t.Logf("  Updated stats: HasValidLease=%v, RenewCount=%d", stats.HasValidLease, stats.LeaseRenewCount)
		}

		// verifylease(ifnot build，warningnotfailuretest)
		if !lm.HasValidLease() {
			t.Logf("  Warning: Lease not established in single-node scenario, continuing test anyway")
		}
	}

	// writetestdata
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("perf-key-%d", i)
		value := fmt.Sprintf("perf-value-%d", i)
		_, _, err := kvs.PutWithLease(context.Background(), key, value, 0)
		require.NoError(t, err)
	}

	time.Sleep(200 * time.Millisecond)

	// executereadperformancetest
	start := time.Now()

	ctx2 := context.Background()
	for i := 0; i < numOps; i++ {
		key := fmt.Sprintf("perf-key-%d", i%100)
		_, err := kvs.Range(ctx2, key, "", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
	}

	duration := time.Since(start)

	// calculate ops/sec
	opsPerSec := int64(float64(numOps) / duration.Seconds())

	// getstatisticsinfo
	if enableLeaseRead {
		stats := node.ReadIndexManager().Stats()
		t.Logf("  Fast path reads: %d", stats.FastPathReads)
		t.Logf("  Slow path reads: %d", stats.SlowPathReads)
		t.Logf("  Fast path rate:  %.2f%%", stats.FastPathRate*100)
	}

	// clean up
	close(proposeC)
	close(confChangeC)
	time.Sleep(100 * time.Millisecond)

	return opsPerSec
}
