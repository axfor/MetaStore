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

// BatchProposal 批量提案包装器
// 用于区分单个提案和批量提案
type BatchProposal struct {
	IsBatch   bool     `json:"is_batch"`   // 是否为批量提案
	Proposals []string `json:"proposals"`  // 提案列表
}

// EncodeBatch 将批量提案编码为 JSON 字节
func EncodeBatch(proposals []string) ([]byte, error) {
	if len(proposals) == 0 {
		return nil, fmt.Errorf("empty proposals")
	}

	// 如果只有一个提案，直接返回原始字符串（向后兼容）
	if len(proposals) == 1 {
		return []byte(proposals[0]), nil
	}

	// 多个提案，编码为批量提案
	batch := BatchProposal{
		IsBatch:   true,
		Proposals: proposals,
	}
	return json.Marshal(batch)
}

// DecodeBatch 解码批量提案
// 返回提案列表。如果是单个提案，返回单元素列表
func DecodeBatch(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// 尝试解码为批量提案
	var batch BatchProposal
	if err := json.Unmarshal(data, &batch); err != nil {
		// 不是批量提案，当作单个提案处理（向后兼容）
		return []string{string(data)}, nil
	}

	// 是批量提案
	if batch.IsBatch {
		return batch.Proposals, nil
	}

	// 不是批量提案标记，当作单个提案
	return []string{string(data)}, nil
}

// IsBatchProposal 检查数据是否为批量提案
func IsBatchProposal(data []byte) bool {
	var batch BatchProposal
	if err := json.Unmarshal(data, &batch); err != nil {
		return false
	}
	return batch.IsBatch
}
