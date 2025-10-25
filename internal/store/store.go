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

package store

// Store is the interface that all KV stores must implement
type Store interface {
	Lookup(key string) (string, bool)
	Propose(k string, v string)
	GetSnapshot() ([]byte, error)
}

// Commit represents a commit event from raft
type Commit struct {
	Data       []string
	ApplyDoneC chan<- struct{}
}

// KV represents a key-value pair
type KV struct {
	Key string
	Val string
}
