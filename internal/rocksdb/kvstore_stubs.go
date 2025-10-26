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

package rocksdb

import (
	"errors"
	"metaStore/internal/kvstore"
)

// 以下方法是为了满足 kvstore.Store 接口的桩实现
// 旧的 RocksDB 实现不支持这些 etcd 特性

func (r *RocksDB) Range(key, rangeEnd string, limit int64, revision int64) (*kvstore.RangeResponse, error) {
	return nil, errors.New("Range not supported in legacy RocksDB store")
}

func (r *RocksDB) PutWithLease(key, value string, leaseID int64) (int64, *kvstore.KeyValue, error) {
	return 0, nil, errors.New("PutWithLease not supported in legacy RocksDB store")
}

func (r *RocksDB) DeleteRange(key, rangeEnd string) (int64, []*kvstore.KeyValue, int64, error) {
	return 0, nil, 0, errors.New("DeleteRange not supported in legacy RocksDB store")
}

func (r *RocksDB) Txn(cmps []kvstore.Compare, thenOps []kvstore.Op, elseOps []kvstore.Op) (*kvstore.TxnResponse, error) {
	return nil, errors.New("Txn not supported in legacy RocksDB store")
}

func (r *RocksDB) Watch(key, rangeEnd string, startRevision int64, watchID int64) (<-chan kvstore.WatchEvent, error) {
	return nil, errors.New("Watch not supported in legacy RocksDB store")
}

func (r *RocksDB) CancelWatch(watchID int64) error {
	return errors.New("CancelWatch not supported in legacy RocksDB store")
}

func (r *RocksDB) Compact(revision int64) error {
	return errors.New("Compact not supported in legacy RocksDB store")
}

func (r *RocksDB) CurrentRevision() int64 {
	return 0
}

func (r *RocksDB) LeaseGrant(id int64, ttl int64) (*kvstore.Lease, error) {
	return nil, errors.New("LeaseGrant not supported in legacy RocksDB store")
}

func (r *RocksDB) LeaseRevoke(id int64) error {
	return errors.New("LeaseRevoke not supported in legacy RocksDB store")
}

func (r *RocksDB) LeaseRenew(id int64) (*kvstore.Lease, error) {
	return nil, errors.New("LeaseRenew not supported in legacy RocksDB store")
}

func (r *RocksDB) LeaseTimeToLive(id int64) (*kvstore.Lease, error) {
	return nil, errors.New("LeaseTimeToLive not supported in legacy RocksDB store")
}

func (r *RocksDB) Leases() ([]*kvstore.Lease, error) {
	return nil, errors.New("Leases not supported in legacy RocksDB store")
}
