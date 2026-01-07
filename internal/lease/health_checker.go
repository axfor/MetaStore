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

package lease

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// HealthChecker test Lease Read systemhealthystatus
type HealthChecker struct {
	leaseManager     *LeaseManager
	readIndexManager *ReadIndexManager
	logger           *zap.Logger

	// config
	checkInterval    time.Duration
	alertThreshold   int // failuretimesthreshold

	// status
	consecutiveFailures int
	lastCheckTime       time.Time
	lastAlertTime       time.Time
}

// HealthStatus Lease Read healthystatus
type HealthStatus struct {
	Healthy           bool
	Issues            []string
	LeaseEstablished  bool
	LeaseRenewRate    float64 // success
	FastPathRate      float64 // fastpathuse
	Timestamp         time.Time
}

// NewHealthChecker createhealthycheck
func NewHealthChecker(lm *LeaseManager, rim *ReadIndexManager, logger *zap.Logger) *HealthChecker {
	return &HealthChecker{
		leaseManager:     lm,
		readIndexManager: rim,
		logger:           logger,
		checkInterval:    30 * time.Second, // default 30 secondscheckfirst time
		alertThreshold:   3,                 //  3  timefailure
	}
}

// Check executehealthycheck
func (hc *HealthChecker) Check() HealthStatus {
	hc.lastCheckTime = time.Now()

	status := HealthStatus{
		Healthy:   true,
		Issues:    make([]string, 0),
		Timestamp: hc.lastCheckTime,
	}

	// check 1: Lease isnobuild(to Leader)
	if hc.leaseManager != nil && hc.leaseManager.IsLeader() {
		leaseStats := hc.leaseManager.Stats()
		status.LeaseEstablished = leaseStats.HasValidLease

		if !status.LeaseEstablished {
			issue := fmt.Sprintf("Leader lease not established (RenewCount=%d, ExpireCount=%d)",
				leaseStats.LeaseRenewCount, leaseStats.LeaseExpireCount)
			status.Issues = append(status.Issues, issue)
			status.Healthy = false

			// ：singlenodecluster
			// ifissinglenode，isknownlimit，degradationaswarning
			if hc.isSingleNodeScenario() {
				hc.logger.Warn("Lease not established in single-node scenario (known limitation)",
					zap.Int64("renew_count", leaseStats.LeaseRenewCount))
				status.Healthy = true // markerashealthy， issue
			}
		}

		// calculatesuccess
		totalAttempts := leaseStats.LeaseRenewCount + leaseStats.LeaseExpireCount
		if totalAttempts > 0 {
			status.LeaseRenewRate = float64(leaseStats.LeaseRenewCount) / float64(totalAttempts)

			// checksuccessisnoedlow
			if status.LeaseRenewRate < 0.8 && totalAttempts > 10 {
				issue := fmt.Sprintf("Low lease renew rate: %.2f%% (expected >80%%)",
					status.LeaseRenewRate*100)
				status.Issues = append(status.Issues, issue)
				status.Healthy = false
			}
		}
	}

	// check 2: ReadIndex use
	if hc.readIndexManager != nil {
		readStats := hc.readIndexManager.Stats()
		status.FastPathRate = readStats.FastPathRate

		// ifhaverequestfastpathuseas 0，mayhave
		if readStats.TotalRequests > 100 && readStats.FastPathRate == 0 {
			// ifnotis Leader orleasenot build，ispositive
			if hc.leaseManager != nil && hc.leaseManager.IsLeader() && hc.leaseManager.HasValidLease() {
				issue := fmt.Sprintf("Fast path not used despite valid lease (Total=%d, FastPath=%d)",
					readStats.TotalRequests, readStats.FastPathReads)
				status.Issues = append(status.Issues, issue)
				status.Healthy = false
			}
		}

		// checkwaithandlerequestisnoedmany
		if readStats.PendingReads > 1000 {
			issue := fmt.Sprintf("Too many pending reads: %d (may indicate performance issue)",
				readStats.PendingReads)
			status.Issues = append(status.Issues, issue)
			status.Healthy = false
		}
	}

	// updatefailurecount
	if !status.Healthy {
		hc.consecutiveFailures++

		// tothresholdwhen
		if hc.consecutiveFailures >= hc.alertThreshold {
			hc.alert(status)
			hc.consecutiveFailures = 0 // resetcount
		}
	} else {
		hc.consecutiveFailures = 0
	}

	return status
}

// isSingleNodeScenario testisnoissinglenodescenarioscene
func (hc *HealthChecker) isSingleNodeScenario() bool {
	// TODO: implementnodetest
	// need Raft clusterconfiginfo
	// currenttransformimplement：iftimesrarelynoneexpiration，mayissinglenode
	stats := hc.leaseManager.Stats()
	return stats.LeaseRenewCount > 0 && stats.LeaseExpireCount == 0 && stats.LeaseRenewCount < 10
}

// alert healthy
func (hc *HealthChecker) alert(status HealthStatus) {
	// (tofewinterval 5 separate)
	if time.Since(hc.lastAlertTime) < 5*time.Minute {
		return
	}

	hc.lastAlertTime = time.Now()

	hc.logger.Error("Lease Read health check failed",
		zap.Bool("healthy", status.Healthy),
		zap.Strings("issues", status.Issues),
		zap.Bool("lease_established", status.LeaseEstablished),
		zap.Float64("lease_renew_rate", status.LeaseRenewRate),
		zap.Float64("fast_path_rate", status.FastPathRate),
		zap.Int("consecutive_failures", hc.consecutiveFailures))

	// TODO: collectbecomealarmsystem
	// - send Prometheus Alert
	// - senditem/short
	// - recordtomonitoringsystem
}

// StartMonitoring startbackgroundhealthymonitoring
func (hc *HealthChecker) StartMonitoring(stopC <-chan struct{}) {
	ticker := time.NewTicker(hc.checkInterval)
	defer ticker.Stop()

	hc.logger.Info("Lease Read health monitoring started",
		zap.Duration("check_interval", hc.checkInterval),
		zap.Int("alert_threshold", hc.alertThreshold))

	for {
		select {
		case <-ticker.C:
			status := hc.Check()

			// recordhealthystatus
			if status.Healthy {
				hc.logger.Debug("Lease Read health check passed",
					zap.Bool("lease_established", status.LeaseEstablished),
					zap.Float64("fast_path_rate", status.FastPathRate))
			} else {
				hc.logger.Warn("Lease Read health check issues detected",
					zap.Strings("issues", status.Issues))
			}

		case <-stopC:
			hc.logger.Info("Lease Read health monitoring stopped")
			return
		}
	}
}

// GetMetrics get Prometheus formatmetrics
func (hc *HealthChecker) GetMetrics() map[string]float64 {
	status := hc.Check()

	metrics := make(map[string]float64)

	// healthystatus(0 = unhealthy，1 = healthy)
	if status.Healthy {
		metrics["lease_read_healthy"] = 1.0
	} else {
		metrics["lease_read_healthy"] = 0.0
	}

	// leasebuildstatus
	if status.LeaseEstablished {
		metrics["lease_established"] = 1.0
	} else {
		metrics["lease_established"] = 0.0
	}

	// success
	metrics["lease_renew_rate"] = status.LeaseRenewRate

	// fastpathuse
	metrics["lease_fast_path_rate"] = status.FastPathRate

	// quantity
	metrics["lease_issues_count"] = float64(len(status.Issues))

	// failuretimes
	metrics["lease_consecutive_failures"] = float64(hc.consecutiveFailures)

	return metrics
}
