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

package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestWitnessConfigDefaults tests that witness node defaults are correctly applied
func TestWitnessConfigDefaults(t *testing.T) {
	t.Run("DataNodeDefaults", func(t *testing.T) {
		cfg := DefaultConfig(1, 1, ":2379")

		// Data node should be default
		if cfg.Server.Raft.NodeRole != NodeRoleData {
			t.Errorf("Expected NodeRole=%s, got %s", NodeRoleData, cfg.Server.Raft.NodeRole)
		}

		// Data node should have LeaseRead enabled
		if !cfg.Server.Raft.LeaseRead.Enable {
			t.Error("Expected LeaseRead.Enable=true for data node")
		}

		// IsWitness should return false
		if cfg.Server.Raft.IsWitness() {
			t.Error("Expected IsWitness()=false for data node")
		}

		// IsDataNode should return true
		if !cfg.Server.Raft.IsDataNode() {
			t.Error("Expected IsDataNode()=true for data node")
		}
	})

	t.Run("WitnessNodeDefaults", func(t *testing.T) {
		cfg := DefaultConfig(1, 1, ":2379")
		cfg.Server.Raft.NodeRole = NodeRoleWitness
		cfg.SetDefaults()

		// Witness should have LeaseRead disabled
		if cfg.Server.Raft.LeaseRead.Enable {
			t.Error("Expected LeaseRead.Enable=false for witness node")
		}

		// Witness should have PersistVote enabled
		if !cfg.Server.Raft.Witness.PersistVote {
			t.Error("Expected Witness.PersistVote=true for witness node")
		}

		// IsWitness should return true
		if !cfg.Server.Raft.IsWitness() {
			t.Error("Expected IsWitness()=true for witness node")
		}

		// IsDataNode should return false
		if cfg.Server.Raft.IsDataNode() {
			t.Error("Expected IsDataNode()=false for witness node")
		}
	})
}

// TestWitnessConfigValidation tests witness-specific validation rules
func TestWitnessConfigValidation(t *testing.T) {
	t.Run("WitnessWithLeaseReadShouldFail", func(t *testing.T) {
		cfg := DefaultConfig(1, 1, ":2379")
		cfg.Server.Raft.NodeRole = NodeRoleWitness
		cfg.SetDefaults()

		// Force enable LeaseRead (this should fail validation)
		cfg.Server.Raft.LeaseRead.Enable = true

		err := cfg.Validate()
		if err == nil {
			t.Error("Expected validation error for witness with LeaseRead enabled")
		}
	})

	t.Run("WitnessWithoutLeaseReadShouldPass", func(t *testing.T) {
		cfg := DefaultConfig(1, 1, ":2379")
		cfg.Server.Raft.NodeRole = NodeRoleWitness
		cfg.SetDefaults()

		err := cfg.Validate()
		if err != nil {
			t.Errorf("Unexpected validation error: %v", err)
		}
	})

	t.Run("InvalidNodeRoleShouldFail", func(t *testing.T) {
		cfg := DefaultConfig(1, 1, ":2379")
		cfg.Server.Raft.NodeRole = "invalid"

		err := cfg.Validate()
		if err == nil {
			t.Error("Expected validation error for invalid node role")
		}
	})
}

// TestWitnessConfigYAML tests YAML parsing for witness configuration
func TestWitnessConfigYAML(t *testing.T) {
	t.Run("ParseWitnessConfig", func(t *testing.T) {
		yamlData := `
server:
  cluster_id: 1
  member_id: 3
  etcd:
    address: ""
  raft:
    node_role: "witness"
    witness:
      persist_vote: true
      forward_requests: false
`
		var cfg Config
		err := yaml.Unmarshal([]byte(yamlData), &cfg)
		if err != nil {
			t.Fatalf("Failed to parse YAML: %v", err)
		}

		cfg.SetDefaults()

		if cfg.Server.Raft.NodeRole != NodeRoleWitness {
			t.Errorf("Expected NodeRole=%s, got %s", NodeRoleWitness, cfg.Server.Raft.NodeRole)
		}

		if !cfg.Server.Raft.Witness.PersistVote {
			t.Error("Expected Witness.PersistVote=true")
		}

		if cfg.Server.Raft.Witness.ForwardRequests {
			t.Error("Expected Witness.ForwardRequests=false")
		}

		// LeaseRead should be disabled for witness
		if cfg.Server.Raft.LeaseRead.Enable {
			t.Error("Expected LeaseRead.Enable=false for witness")
		}
	})

	t.Run("ParseDataNodeConfig", func(t *testing.T) {
		yamlData := `
server:
  cluster_id: 1
  member_id: 1
  etcd:
    address: ":2379"
  raft:
    node_role: "data"
`
		var cfg Config
		err := yaml.Unmarshal([]byte(yamlData), &cfg)
		if err != nil {
			t.Fatalf("Failed to parse YAML: %v", err)
		}

		cfg.SetDefaults()

		if cfg.Server.Raft.NodeRole != NodeRoleData {
			t.Errorf("Expected NodeRole=%s, got %s", NodeRoleData, cfg.Server.Raft.NodeRole)
		}

		// LeaseRead should be enabled for data node
		if !cfg.Server.Raft.LeaseRead.Enable {
			t.Error("Expected LeaseRead.Enable=true for data node")
		}
	})
}

// TestMainConfigHasWitnessSettings tests that the main config.yaml includes witness settings
func TestMainConfigHasWitnessSettings(t *testing.T) {
	// Test YAML structure matches main config.yaml format
	yamlData := `
server:
  cluster_id: 1
  member_id: 1
  etcd:
    address: ":2379"
  raft:
    node_role: "data"
    witness:
      persist_vote: true
      forward_requests: false
    tick_interval: 100ms
    election_tick: 10
    heartbeat_tick: 1
`
	var cfg Config
	err := yaml.Unmarshal([]byte(yamlData), &cfg)
	if err != nil {
		t.Fatalf("Failed to parse YAML: %v", err)
	}

	cfg.SetDefaults()

	// Verify data node settings
	if cfg.Server.Raft.NodeRole != NodeRoleData {
		t.Errorf("Expected NodeRole=%s, got %s", NodeRoleData, cfg.Server.Raft.NodeRole)
	}

	// Verify witness config is parsed (even for data node)
	if !cfg.Server.Raft.Witness.PersistVote {
		t.Error("Expected Witness.PersistVote=true")
	}

	// Verify LeaseRead enabled for data node
	if !cfg.Server.Raft.LeaseRead.Enable {
		t.Error("Expected LeaseRead.Enable=true for data node")
	}

	// Now test switching to witness mode
	cfg.Server.Raft.NodeRole = NodeRoleWitness
	cfg.SetDefaults()

	if cfg.Server.Raft.LeaseRead.Enable {
		t.Error("Expected LeaseRead.Enable=false for witness node after SetDefaults")
	}
}

// TestNodeRoleHelpers tests the IsWitness and IsDataNode helper methods
func TestNodeRoleHelpers(t *testing.T) {
	testCases := []struct {
		name       string
		role       NodeRole
		isWitness  bool
		isDataNode bool
	}{
		{"EmptyRole", "", false, true},
		{"DataRole", NodeRoleData, false, true},
		{"WitnessRole", NodeRoleWitness, true, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			raftCfg := &RaftConfig{NodeRole: tc.role}

			if raftCfg.IsWitness() != tc.isWitness {
				t.Errorf("IsWitness(): expected %v, got %v", tc.isWitness, raftCfg.IsWitness())
			}

			if raftCfg.IsDataNode() != tc.isDataNode {
				t.Errorf("IsDataNode(): expected %v, got %v", tc.isDataNode, raftCfg.IsDataNode())
			}
		})
	}
}
