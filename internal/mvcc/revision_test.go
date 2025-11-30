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
	"testing"
)

func TestRevisionCompare(t *testing.T) {
	tests := []struct {
		name     string
		r1, r2   Revision
		expected int
	}{
		{"equal", Revision{1, 0}, Revision{1, 0}, 0},
		{"main less", Revision{1, 0}, Revision{2, 0}, -1},
		{"main greater", Revision{2, 0}, Revision{1, 0}, 1},
		{"sub less", Revision{1, 0}, Revision{1, 1}, -1},
		{"sub greater", Revision{1, 1}, Revision{1, 0}, 1},
		{"main takes priority", Revision{2, 0}, Revision{1, 5}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r1.Compare(tt.r2); got != tt.expected {
				t.Errorf("Compare() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRevisionComparisons(t *testing.T) {
	r1 := Revision{1, 0}
	r2 := Revision{2, 0}

	if !r1.LessThan(r2) {
		t.Error("expected r1 < r2")
	}
	if !r1.LessThanOrEqual(r2) {
		t.Error("expected r1 <= r2")
	}
	if !r2.GreaterThan(r1) {
		t.Error("expected r2 > r1")
	}
	if !r2.GreaterThanOrEqual(r1) {
		t.Error("expected r2 >= r1")
	}
	if !r1.LessThanOrEqual(r1) {
		t.Error("expected r1 <= r1")
	}
	if !r1.GreaterThanOrEqual(r1) {
		t.Error("expected r1 >= r1")
	}
}

func TestRevisionBytes(t *testing.T) {
	tests := []struct {
		name string
		rev  Revision
	}{
		{"zero", Revision{0, 0}},
		{"simple", Revision{1, 2}},
		{"large", Revision{1234567890, 9876543210}},
		{"max", Revision{1<<63 - 1, 1<<63 - 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytes := tt.rev.Bytes()
			if len(bytes) != RevisionSize {
				t.Errorf("Bytes() length = %d, want %d", len(bytes), RevisionSize)
			}

			parsed := ParseRevision(bytes)
			if parsed != tt.rev {
				t.Errorf("ParseRevision() = %v, want %v", parsed, tt.rev)
			}
		})
	}
}

func TestRevisionEncodeTo(t *testing.T) {
	rev := Revision{123, 456}
	buf := make([]byte, RevisionSize)
	rev.EncodeTo(buf)

	parsed := ParseRevision(buf)
	if parsed != rev {
		t.Errorf("EncodeTo/Parse = %v, want %v", parsed, rev)
	}
}

func TestRevisionIsZero(t *testing.T) {
	if !Zero.IsZero() {
		t.Error("Zero.IsZero() should be true")
	}
	if !(Revision{0, 0}).IsZero() {
		t.Error("{0,0}.IsZero() should be true")
	}
	if (Revision{1, 0}).IsZero() {
		t.Error("{1,0}.IsZero() should be false")
	}
	if (Revision{0, 1}).IsZero() {
		t.Error("{0,1}.IsZero() should be false")
	}
}

func TestRevisionString(t *testing.T) {
	rev := Revision{1, 2}
	s := rev.String()
	if s != "{main: 1, sub: 2}" {
		t.Errorf("String() = %q, want %q", s, "{main: 1, sub: 2}")
	}
}

func TestParseRevisionInvalid(t *testing.T) {
	// Too short
	short := []byte{1, 2, 3}
	if ParseRevision(short) != Zero {
		t.Error("ParseRevision with short data should return Zero")
	}
}

func TestNewRevision(t *testing.T) {
	rev := NewRevision(10, 20)
	if rev.Main != 10 || rev.Sub != 20 {
		t.Errorf("NewRevision() = %v, want {10, 20}", rev)
	}
}

func TestRevisionRange(t *testing.T) {
	rr := RevisionRange{
		Start: Revision{1, 0},
		End:   Revision{5, 0},
	}

	tests := []struct {
		rev      Revision
		contains bool
	}{
		{Revision{0, 0}, false},
		{Revision{1, 0}, true},
		{Revision{3, 5}, true},
		{Revision{4, 99}, true},
		{Revision{5, 0}, false},
		{Revision{6, 0}, false},
	}

	for _, tt := range tests {
		if got := rr.Contains(tt.rev); got != tt.contains {
			t.Errorf("Contains(%v) = %v, want %v", tt.rev, got, tt.contains)
		}
	}
}

func TestRevisionGenerator(t *testing.T) {
	gen := NewRevisionGenerator(Revision{0, 0})

	// First Next should give {1, 0}
	r1 := gen.Next()
	if r1 != (Revision{1, 0}) {
		t.Errorf("Next() = %v, want {1, 0}", r1)
	}

	// Current should return same
	if gen.Current() != r1 {
		t.Error("Current() should return last generated revision")
	}

	// NextSub should increment sub
	r2 := gen.NextSub()
	if r2 != (Revision{1, 1}) {
		t.Errorf("NextSub() = %v, want {1, 1}", r2)
	}

	// Next should increment main and reset sub
	r3 := gen.Next()
	if r3 != (Revision{2, 0}) {
		t.Errorf("Next() = %v, want {2, 0}", r3)
	}

	// SetMain
	gen.SetMain(100)
	if gen.Current() != (Revision{100, 0}) {
		t.Errorf("after SetMain(100), Current() = %v, want {100, 0}", gen.Current())
	}
}
