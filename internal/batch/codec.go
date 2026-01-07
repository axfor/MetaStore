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

// BatchProposal package
// for separatesingle and
type BatchProposal struct {
	IsBatch   bool     `json:"is_batch"`   // isnoas
	Proposals []string `json:"proposals"`  // list
}

// EncodeBatch willencodeas JSON 
func EncodeBatch(proposals []string) ([]byte, error) {
	if len(proposals) == 0 {
		return nil, fmt.Errorf("empty proposals")
	}

	// ifhavefirst ，returnstart(aftercompatible)
	if len(proposals) == 1 {
		return []byte(proposals[0]), nil
	}

	// many ，encodeas
	batch := BatchProposal{
		IsBatch:   true,
		Proposals: proposals,
	}
	return json.Marshal(batch)
}

// DecodeBatch decode
// returnlist。ifissingle ，returnunitlist
func DecodeBatch(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// testdecodeas
	var batch BatchProposal
	if err := json.Unmarshal(data, &batch); err != nil {
		// notis，whensingle handle(aftercompatible)
		return []string{string(data)}, nil
	}

	// is
	if batch.IsBatch {
		return batch.Proposals, nil
	}

	// notismarker，whensingle 
	return []string{string(data)}, nil
}

// IsBatchProposal checkdataisnoas
func IsBatchProposal(data []byte) bool {
	var batch BatchProposal
	if err := json.Unmarshal(data, &batch); err != nil {
		return false
	}
	return batch.IsBatch
}
