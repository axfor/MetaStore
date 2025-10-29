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

//go:build !windows
// +build !windows

package health

import (
	"fmt"
	"syscall"
)

// getDiskUsage returns disk usage information for a given path
// Returns: totalGB, freeGB, usedPercent, error
func getDiskUsage(path string) (float64, float64, float64, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to get disk stats: %w", err)
	}

	// Calculate sizes
	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bfree * uint64(stat.Bsize)
	usedBytes := totalBytes - freeBytes

	// Convert to GB
	totalGB := float64(totalBytes) / (1024 * 1024 * 1024)
	freeGB := float64(freeBytes) / (1024 * 1024 * 1024)

	// Calculate usage percentage
	usedPercent := float64(usedBytes) / float64(totalBytes) * 100

	return totalGB, freeGB, usedPercent, nil
}
