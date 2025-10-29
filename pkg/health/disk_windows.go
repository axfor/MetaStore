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

//go:build windows
// +build windows

package health

import (
	"fmt"
)

// getDiskUsage returns disk usage information for a given path on Windows
// Returns: totalGB, freeGB, usedPercent, error
func getDiskUsage(path string) (float64, float64, float64, error) {
	// Windows implementation would use kernel32.GetDiskFreeSpaceEx
	// For now, return a placeholder
	return 0, 0, 0, fmt.Errorf("disk usage check not implemented on Windows")
}
