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
	"testing"
)

// TestEncodeBatch_SingleProposal tests encoding a single proposal
func TestEncodeBatch_SingleProposal(t *testing.T) {
	proposals := []string{"single-proposal"}

	data, err := EncodeBatch(proposals)
	if err != nil {
		t.Fatalf("EncodeBatch failed: %v", err)
	}

	// Single proposal should not be wrapped
	if string(data) != "single-proposal" {
		t.Errorf("Single proposal encoding incorrect: got %s, want %s", string(data), "single-proposal")
	}

	// Should not be detected as batch
	if IsBatchProposal(data) {
		t.Error("Single proposal incorrectly detected as batch")
	}
}

// TestEncodeBatch_MultipleProposals tests encoding multiple proposals
func TestEncodeBatch_MultipleProposals(t *testing.T) {
	proposals := []string{"prop1", "prop2", "prop3"}

	data, err := EncodeBatch(proposals)
	if err != nil {
		t.Fatalf("EncodeBatch failed: %v", err)
	}

	// Multiple proposals should be detected as batch
	if !IsBatchProposal(data) {
		t.Error("Multiple proposals not detected as batch")
	}

	// Decode and verify
	decoded, err := DecodeBatch(data)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}

	if len(decoded) != len(proposals) {
		t.Errorf("Decoded length mismatch: got %d, want %d", len(decoded), len(proposals))
	}

	for i, prop := range proposals {
		if decoded[i] != prop {
			t.Errorf("Proposal %d mismatch: got %s, want %s", i, decoded[i], prop)
		}
	}
}

// TestEncodeBatch_EmptyProposals tests encoding empty proposals
func TestEncodeBatch_EmptyProposals(t *testing.T) {
	proposals := []string{}

	_, err := EncodeBatch(proposals)
	if err == nil {
		t.Error("EncodeBatch should fail for empty proposals")
	}
}

// TestDecodeBatch_SingleProposal tests decoding a single proposal
func TestDecodeBatch_SingleProposal(t *testing.T) {
	data := []byte("single-proposal")

	decoded, err := DecodeBatch(data)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}

	if len(decoded) != 1 {
		t.Errorf("Decoded length incorrect: got %d, want 1", len(decoded))
	}

	if decoded[0] != "single-proposal" {
		t.Errorf("Decoded value incorrect: got %s, want %s", decoded[0], "single-proposal")
	}
}

// TestDecodeBatch_MultipleProposals tests decoding multiple proposals
func TestDecodeBatch_MultipleProposals(t *testing.T) {
	proposals := []string{"prop1", "prop2", "prop3"}

	// Encode first
	data, err := EncodeBatch(proposals)
	if err != nil {
		t.Fatalf("EncodeBatch failed: %v", err)
	}

	// Decode
	decoded, err := DecodeBatch(data)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}

	if len(decoded) != len(proposals) {
		t.Errorf("Decoded length mismatch: got %d, want %d", len(decoded), len(proposals))
	}

	for i, prop := range proposals {
		if decoded[i] != prop {
			t.Errorf("Proposal %d mismatch: got %s, want %s", i, decoded[i], prop)
		}
	}
}

// TestDecodeBatch_InvalidJSON tests decoding invalid JSON
// Invalid JSON is treated as a single proposal for backward compatibility
func TestDecodeBatch_InvalidJSON(t *testing.T) {
	data := []byte("{invalid json")

	decoded, err := DecodeBatch(data)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}

	// Invalid JSON should be treated as a single proposal (backward compatibility)
	if len(decoded) != 1 {
		t.Errorf("Expected 1 proposal, got %d", len(decoded))
	}

	if decoded[0] != string(data) {
		t.Errorf("Proposal mismatch: got %s, want %s", decoded[0], string(data))
	}
}

// TestIsBatchProposal tests batch detection
func TestIsBatchProposal(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{
			name:     "single proposal",
			data:     []byte("single-proposal"),
			expected: false,
		},
		{
			name:     "batch proposal",
			data:     []byte(`{"is_batch":true,"proposals":["p1","p2"]}`),
			expected: true,
		},
		{
			name:     "invalid json",
			data:     []byte("{invalid"),
			expected: false,
		},
		{
			name:     "batch false",
			data:     []byte(`{"is_batch":false,"proposals":["p1"]}`),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsBatchProposal(tt.data)
			if result != tt.expected {
				t.Errorf("IsBatchProposal() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestEncodeDecode_RoundTrip tests encode-decode round trip
func TestEncodeDecode_RoundTrip(t *testing.T) {
	testCases := []struct {
		name      string
		proposals []string
	}{
		{
			name:      "single proposal",
			proposals: []string{"single"},
		},
		{
			name:      "two proposals",
			proposals: []string{"prop1", "prop2"},
		},
		{
			name:      "many proposals",
			proposals: []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"},
		},
		{
			name:      "proposals with special characters",
			proposals: []string{`{"key":"value"}`, `[1,2,3]`, `"quoted"`},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			data, err := EncodeBatch(tc.proposals)
			if err != nil {
				t.Fatalf("EncodeBatch failed: %v", err)
			}

			// Decode
			decoded, err := DecodeBatch(data)
			if err != nil {
				t.Fatalf("DecodeBatch failed: %v", err)
			}

			// Verify
			if len(decoded) != len(tc.proposals) {
				t.Errorf("Length mismatch: got %d, want %d", len(decoded), len(tc.proposals))
			}

			for i, prop := range tc.proposals {
				if decoded[i] != prop {
					t.Errorf("Proposal %d mismatch: got %s, want %s", i, decoded[i], prop)
				}
			}
		})
	}
}

// TestEncodeBatch_LargeBatch tests encoding a large batch
func TestEncodeBatch_LargeBatch(t *testing.T) {
	// Create a large batch (256 proposals)
	proposals := make([]string, 256)
	for i := range proposals {
		proposals[i] = "proposal-" + string(rune(i))
	}

	data, err := EncodeBatch(proposals)
	if err != nil {
		t.Fatalf("EncodeBatch failed: %v", err)
	}

	// Should be detected as batch
	if !IsBatchProposal(data) {
		t.Error("Large batch not detected as batch")
	}

	// Decode and verify
	decoded, err := DecodeBatch(data)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}

	if len(decoded) != len(proposals) {
		t.Errorf("Decoded length mismatch: got %d, want %d", len(decoded), len(proposals))
	}
}
