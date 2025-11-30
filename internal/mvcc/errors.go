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

import "errors"

var (
	// ErrKeyNotFound is returned when a key is not found in the store.
	ErrKeyNotFound = errors.New("mvcc: key not found")

	// ErrRevisionNotFound is returned when a revision is not found.
	ErrRevisionNotFound = errors.New("mvcc: revision not found")

	// ErrCompacted is returned when the requested revision has been compacted.
	ErrCompacted = errors.New("mvcc: required revision has been compacted")

	// ErrFutureRevision is returned when the requested revision is greater than current.
	ErrFutureRevision = errors.New("mvcc: required revision is a future revision")

	// ErrInvalidData is returned when data cannot be decoded.
	ErrInvalidData = errors.New("mvcc: invalid data format")

	// ErrEmptyKey is returned when an empty key is provided.
	ErrEmptyKey = errors.New("mvcc: empty key is not allowed")

	// ErrTxnTooBig is returned when a transaction exceeds the size limit.
	ErrTxnTooBig = errors.New("mvcc: transaction is too big")

	// ErrClosed is returned when operating on a closed store.
	ErrClosed = errors.New("mvcc: store is closed")
)
