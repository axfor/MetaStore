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

// QueryPlan represents a parsed SQL query execution plan
type QueryPlan struct {
	Type      QueryType
	TableName string
	Columns   []string        // SELECT columns
	Where     *WhereCondition // WHERE clause
	Limit     int64
	Offset    int64
}

// QueryType represents the type of SQL query
type QueryType int

const (
	QueryTypeSelect QueryType = iota
	QueryTypeInsert
	QueryTypeUpdate
	QueryTypeDelete
)

// WhereCondition represents WHERE clause conditions
type WhereCondition struct {
	Type     ConditionType
	Key      string   // For simple conditions
	Value    interface{} // String, int64, etc.
	Operator string   // =, >, <, >=, <=, LIKE, IN, !=
	IsLike   bool
	Prefix   string              // For LIKE 'prefix%'
	Children []*WhereCondition   // For AND/OR
	InValues []interface{}       // For IN clause
}

// ConditionType represents the type of WHERE condition
type ConditionType int

const (
	ConditionTypeSimple ConditionType = iota // key = 'value'
	ConditionTypeAnd                         // expr AND expr
	ConditionTypeOr                          // expr OR expr
	ConditionTypeIn                          // key IN (...)
)

// String returns the string representation of QueryType
func (qt QueryType) String() string {
	switch qt {
	case QueryTypeSelect:
		return "SELECT"
	case QueryTypeInsert:
		return "INSERT"
	case QueryTypeUpdate:
		return "UPDATE"
	case QueryTypeDelete:
		return "DELETE"
	default:
		return "UNKNOWN"
	}
}

// String returns the string representation of ConditionType
func (ct ConditionType) String() string {
	switch ct {
	case ConditionTypeSimple:
		return "SIMPLE"
	case ConditionTypeAnd:
		return "AND"
	case ConditionTypeOr:
		return "OR"
	case ConditionTypeIn:
		return "IN"
	default:
		return "UNKNOWN"
	}
}
