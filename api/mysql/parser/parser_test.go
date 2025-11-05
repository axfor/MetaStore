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

package parser

import (
	"testing"
)

func TestSQLParser_SimpleSelect(t *testing.T) {
	parser := NewSQLParser()

	tests := []struct {
		name        string
		sql         string
		wantType    QueryType
		wantTable   string
		wantColumns []string
		wantErr     bool
	}{
		{
			name:        "SELECT * FROM kv",
			sql:         "SELECT * FROM kv",
			wantType:    QueryTypeSelect,
			wantTable:   "kv",
			wantColumns: []string{"*"},
			wantErr:     false,
		},
		{
			name:        "SELECT `key` FROM kv",
			sql:         "SELECT `key` FROM kv",
			wantType:    QueryTypeSelect,
			wantTable:   "kv",
			wantColumns: []string{"key"},
			wantErr:     false,
		},
		{
			name:        "SELECT `key`, `value` FROM kv",
			sql:         "SELECT `key`, `value` FROM kv",
			wantType:    QueryTypeSelect,
			wantTable:   "kv",
			wantColumns: []string{"key", "value"},
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parser.Parse(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if plan.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", plan.Type, tt.wantType)
			}
			if plan.TableName != tt.wantTable {
				t.Errorf("TableName = %v, want %v", plan.TableName, tt.wantTable)
			}
			if len(plan.Columns) != len(tt.wantColumns) {
				t.Errorf("Columns count = %v, want %v", len(plan.Columns), len(tt.wantColumns))
				return
			}
			for i, col := range plan.Columns {
				if col != tt.wantColumns[i] {
					t.Errorf("Columns[%d] = %v, want %v", i, col, tt.wantColumns[i])
				}
			}
		})
	}
}

func TestSQLParser_WhereClause(t *testing.T) {
	parser := NewSQLParser()

	tests := []struct {
		name          string
		sql           string
		wantWhereType ConditionType
		wantKey       string
		wantValue     interface{}
		wantOp        string
		wantErr       bool
	}{
		{
			name:          "WHERE `key` = 'test'",
			sql:           "SELECT * FROM kv WHERE `key` = 'test'",
			wantWhereType: ConditionTypeSimple,
			wantKey:       "key",
			wantValue:     "test",
			wantOp:        "eq",
			wantErr:       false,
		},
		{
			name:          "WHERE `key` LIKE 'prefix%'",
			sql:           "SELECT * FROM kv WHERE `key` LIKE 'prefix%'",
			wantWhereType: ConditionTypeSimple,
			wantKey:       "key",
			wantValue:     "prefix%",
			wantOp:        "LIKE",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parser.Parse(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if plan.Where == nil {
				t.Error("WHERE clause is nil")
				return
			}

			if plan.Where.Type != tt.wantWhereType {
				t.Errorf("WHERE Type = %v, want %v", plan.Where.Type, tt.wantWhereType)
			}
			if plan.Where.Key != tt.wantKey {
				t.Errorf("WHERE Key = %v, want %v", plan.Where.Key, tt.wantKey)
			}
			if plan.Where.Value != tt.wantValue {
				t.Errorf("WHERE Value = %v, want %v", plan.Where.Value, tt.wantValue)
			}
		})
	}
}

func TestSQLParser_ComplexWhere(t *testing.T) {
	parser := NewSQLParser()

	tests := []struct {
		name          string
		sql           string
		wantWhereType ConditionType
		wantChildren  int
		wantErr       bool
	}{
		{
			name:          "WHERE with AND",
			sql:           "SELECT * FROM kv WHERE `key` = 'k1' AND `value` = 'v1'",
			wantWhereType: ConditionTypeAnd,
			wantChildren:  2,
			wantErr:       false,
		},
		{
			name:          "WHERE with OR",
			sql:           "SELECT * FROM kv WHERE `key` = 'k1' OR `key` = 'k2'",
			wantWhereType: ConditionTypeOr,
			wantChildren:  2,
			wantErr:       false,
		},
		{
			name:          "WHERE with parentheses",
			sql:           "SELECT * FROM kv WHERE (`key` = 'k1' OR `key` = 'k2') AND `value` = 'v'",
			wantWhereType: ConditionTypeAnd,
			wantChildren:  2,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parser.Parse(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if plan.Where == nil {
				t.Error("WHERE clause is nil")
				return
			}

			if plan.Where.Type != tt.wantWhereType {
				t.Errorf("WHERE Type = %v, want %v", plan.Where.Type, tt.wantWhereType)
			}
			if len(plan.Where.Children) != tt.wantChildren {
				t.Errorf("WHERE Children count = %v, want %v", len(plan.Where.Children), tt.wantChildren)
			}
		})
	}
}

func TestSQLParser_InClause(t *testing.T) {
	parser := NewSQLParser()

	plan, err := parser.Parse("SELECT * FROM kv WHERE `key` IN ('k1', 'k2', 'k3')")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if plan.Where == nil {
		t.Fatal("WHERE clause is nil")
	}

	if plan.Where.Type != ConditionTypeIn {
		t.Errorf("WHERE Type = %v, want %v", plan.Where.Type, ConditionTypeIn)
	}

	if plan.Where.Key != "key" {
		t.Errorf("WHERE Key = %v, want 'key'", plan.Where.Key)
	}

	if len(plan.Where.InValues) != 3 {
		t.Errorf("IN values count = %v, want 3", len(plan.Where.InValues))
	}

	expectedValues := []string{"k1", "k2", "k3"}
	for i, val := range plan.Where.InValues {
		if val != expectedValues[i] {
			t.Errorf("IN value[%d] = %v, want %v", i, val, expectedValues[i])
		}
	}
}

func TestSQLParser_Limit(t *testing.T) {
	parser := NewSQLParser()

	tests := []struct {
		name       string
		sql        string
		wantLimit  int64
		wantOffset int64
	}{
		{
			name:       "LIMIT 10",
			sql:        "SELECT * FROM kv LIMIT 10",
			wantLimit:  10,
			wantOffset: 0,
		},
		{
			name:       "LIMIT 10 OFFSET 5",
			sql:        "SELECT * FROM kv LIMIT 10 OFFSET 5",
			wantLimit:  10,
			wantOffset: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := parser.Parse(tt.sql)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			if plan.Limit != tt.wantLimit {
				t.Errorf("Limit = %v, want %v", plan.Limit, tt.wantLimit)
			}
			if plan.Offset != tt.wantOffset {
				t.Errorf("Offset = %v, want %v", plan.Offset, tt.wantOffset)
			}
		})
	}
}
