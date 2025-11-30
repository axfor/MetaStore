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

package mvcc

import (
	"encoding/binary"
	"fmt"
)

const (
	// RevisionSize is the byte size of a revision (main + sub = 8 + 8 = 16 bytes)
	RevisionSize = 16
)

// Revision represents a unique version identifier in MVCC.
// It consists of a main revision (incremented per transaction) and
// a sub revision (incremented per operation within a transaction).
// This is compatible with etcd's revision model.
type Revision struct {
	// Main is the main revision number, incremented for each transaction.
	Main int64

	// Sub is the sub revision number, incremented for each operation within a transaction.
	// Starts from 0 for each new main revision.
	Sub int64
}

// Zero is the zero revision, used as a sentinel value.
var Zero = Revision{}

// Compare compares two revisions.
// Returns -1 if r < other, 0 if r == other, 1 if r > other.
func (r Revision) Compare(other Revision) int {
	if r.Main < other.Main {
		return -1
	}
	if r.Main > other.Main {
		return 1
	}
	if r.Sub < other.Sub {
		return -1
	}
	if r.Sub > other.Sub {
		return 1
	}
	return 0
}

// GreaterThan returns true if r > other.
func (r Revision) GreaterThan(other Revision) bool {
	return r.Compare(other) > 0
}

// GreaterThanOrEqual returns true if r >= other.
func (r Revision) GreaterThanOrEqual(other Revision) bool {
	return r.Compare(other) >= 0
}

// LessThan returns true if r < other.
func (r Revision) LessThan(other Revision) bool {
	return r.Compare(other) < 0
}

// LessThanOrEqual returns true if r <= other.
func (r Revision) LessThanOrEqual(other Revision) bool {
	return r.Compare(other) <= 0
}

// IsZero returns true if the revision is zero.
func (r Revision) IsZero() bool {
	return r.Main == 0 && r.Sub == 0
}

// Bytes encodes the revision to a 16-byte slice.
// Uses big-endian encoding for lexicographic ordering in storage.
func (r Revision) Bytes() []byte {
	buf := make([]byte, RevisionSize)
	binary.BigEndian.PutUint64(buf[0:8], uint64(r.Main))
	binary.BigEndian.PutUint64(buf[8:16], uint64(r.Sub))
	return buf
}

// EncodeTo encodes the revision into the provided buffer.
// The buffer must be at least RevisionSize bytes.
func (r Revision) EncodeTo(buf []byte) {
	binary.BigEndian.PutUint64(buf[0:8], uint64(r.Main))
	binary.BigEndian.PutUint64(buf[8:16], uint64(r.Sub))
}

// String returns a string representation of the revision.
func (r Revision) String() string {
	return fmt.Sprintf("{main: %d, sub: %d}", r.Main, r.Sub)
}

// ParseRevision decodes a revision from a 16-byte slice.
func ParseRevision(b []byte) Revision {
	if len(b) < RevisionSize {
		return Zero
	}
	return Revision{
		Main: int64(binary.BigEndian.Uint64(b[0:8])),
		Sub:  int64(binary.BigEndian.Uint64(b[8:16])),
	}
}

// NewRevision creates a new revision with the given main and sub values.
func NewRevision(main, sub int64) Revision {
	return Revision{Main: main, Sub: sub}
}

// RevisionRange represents a range of revisions [Start, End).
type RevisionRange struct {
	Start Revision
	End   Revision
}

// Contains returns true if the given revision is within the range [Start, End).
func (rr RevisionRange) Contains(r Revision) bool {
	return r.GreaterThanOrEqual(rr.Start) && r.LessThan(rr.End)
}

// RevisionGenerator generates monotonically increasing revisions.
type RevisionGenerator struct {
	current Revision
}

// NewRevisionGenerator creates a new revision generator starting from the given revision.
func NewRevisionGenerator(start Revision) *RevisionGenerator {
	return &RevisionGenerator{current: start}
}

// Current returns the current revision without incrementing.
func (g *RevisionGenerator) Current() Revision {
	return g.current
}

// Next increments the main revision and resets sub to 0.
// Returns the new revision.
func (g *RevisionGenerator) Next() Revision {
	g.current.Main++
	g.current.Sub = 0
	return g.current
}

// NextSub increments only the sub revision.
// Returns the new revision.
func (g *RevisionGenerator) NextSub() Revision {
	g.current.Sub++
	return g.current
}

// SetMain sets the main revision. Sub is reset to 0.
func (g *RevisionGenerator) SetMain(main int64) {
	g.current.Main = main
	g.current.Sub = 0
}
