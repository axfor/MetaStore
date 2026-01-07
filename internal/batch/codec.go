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

package batch

import (
	"encoding/json"
	"fmt"
)

// BatchProposal 批量提案package装器
// 用at区分单个提案and批量提案
type BatchProposal struct {
	IsBatch   bool     `json:"is_batch"`   // isnoas批量提案
	Proposals []string `json:"proposals"`  // 提案list
}

// EncodeBatch will批量提案encodeas JSON 字节
func EncodeBatch(proposals []string) ([]byte, error) {
	if len(proposals) == 0 {
		return nil, fmt.Errorf("empty proposals")
	}

	// if只有一个提案，直接return原始字符串（向后compatible）
	if len(proposals) == 1 {
		return []byte(proposals[0]), nil
	}

	// 多个提案，encodeas批量提案
	batch := BatchProposal{
		IsBatch:   true,
		Proposals: proposals,
	}
	return json.Marshal(batch)
}

// DecodeBatch decode批量提案
// return提案list。ifis单个提案，returnunit素list
func DecodeBatch(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// 尝试decodeas批量提案
	var batch BatchProposal
	if err := json.Unmarshal(data, &batch); err != nil {
		// notis批量提案，when作单个提案handle（向后compatible）
		return []string{string(data)}, nil
	}

	// is批量提案
	if batch.IsBatch {
		return batch.Proposals, nil
	}

	// notis批量提案marker，when作单个提案
	return []string{string(data)}, nil
}

// IsBatchProposal checkdataisnoas批量提案
func IsBatchProposal(data []byte) bool {
	var batch BatchProposal
	if err := json.Unmarshal(data, &batch); err != nil {
		return false
	}
	return batch.IsBatch
}
