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

package etcd

import (
	"sync"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

// AlarmManager 管理集群告警
type AlarmManager struct {
	mu     sync.RWMutex
	alarms map[uint64]*pb.AlarmMember // memberID -> alarm
}

// NewAlarmManager 创建告警管理器
func NewAlarmManager() *AlarmManager {
	return &AlarmManager{
		alarms: make(map[uint64]*pb.AlarmMember),
	}
}

// Activate 激活告警
func (am *AlarmManager) Activate(alarm *pb.AlarmMember) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.alarms[alarm.MemberID] = alarm
}

// Deactivate 取消告警
func (am *AlarmManager) Deactivate(memberID uint64, alarmType pb.AlarmType) {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	if alarm, exists := am.alarms[memberID]; exists {
		if alarm.Alarm == alarmType || alarmType == pb.AlarmType_NONE {
			delete(am.alarms, memberID)
		}
	}
}

// Get 获取指定成员的告警
func (am *AlarmManager) Get(memberID uint64) *pb.AlarmMember {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return am.alarms[memberID]
}

// List 列出所有告警
func (am *AlarmManager) List() []*pb.AlarmMember {
	am.mu.RLock()
	defer am.mu.RUnlock()
	
	alarms := make([]*pb.AlarmMember, 0, len(am.alarms))
	for _, alarm := range am.alarms {
		alarms = append(alarms, alarm)
	}
	return alarms
}

// CheckStorageQuota 检查存储配额
func (am *AlarmManager) CheckStorageQuota(memberID uint64, dbSize int64, quotaBytes int64) {
	if quotaBytes <= 0 {
		return // 未设置配额
	}
	
	if dbSize >= quotaBytes {
		// 触发 NOSPACE 告警
		alarm := &pb.AlarmMember{
			MemberID: memberID,
			Alarm:    pb.AlarmType_NOSPACE,
		}
		am.Activate(alarm)
	} else if dbSize < int64(float64(quotaBytes)*0.9) {
		// 如果使用率低于 90%，取消告警
		am.Deactivate(memberID, pb.AlarmType_NOSPACE)
	}
}

// HasAlarm 检查是否有指定类型的告警
func (am *AlarmManager) HasAlarm(alarmType pb.AlarmType) bool {
	am.mu.RLock()
	defer am.mu.RUnlock()
	
	for _, alarm := range am.alarms {
		if alarmType == pb.AlarmType_NONE || alarm.Alarm == alarmType {
			return true
		}
	}
	return false
}
