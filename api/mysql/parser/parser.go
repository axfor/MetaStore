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
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/opcode"
	"github.com/pingcap/tidb/pkg/parser/test_driver"
)

// SQLParser wraps TiDB parser for SQL parsing
type SQLParser struct {
	parser *parser.Parser
}

// NewSQLParser creates a new SQL parser instance
func NewSQLParser() *SQLParser {
	return &SQLParser{
		parser: parser.New(),
	}
}

// Parse parses a SQL query and returns a query plan
func (p *SQLParser) Parse(sql string) (*QueryPlan, error) {
	stmts, _, err := p.parser.Parse(sql, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to parse SQL: %w", err)
	}

	if len(stmts) == 0 {
		return nil, fmt.Errorf("no statement found")
	}

	// Currently only support the first statement
	stmt := stmts[0]

	switch stmt := stmt.(type) {
	case *ast.SelectStmt:
		return p.parseSelectStmt(stmt)
	case *ast.InsertStmt:
		return p.parseInsertStmt(stmt)
	case *ast.UpdateStmt:
		return p.parseUpdateStmt(stmt)
	case *ast.DeleteStmt:
		return p.parseDeleteStmt(stmt)
	default:
		return nil, fmt.Errorf("unsupported statement type: %T", stmt)
	}
}

// parseSelectStmt parses a SELECT statement
func (p *SQLParser) parseSelectStmt(stmt *ast.SelectStmt) (*QueryPlan, error) {
	plan := &QueryPlan{
		Type: QueryTypeSelect,
	}

	// Parse table name from FROM clause
	if stmt.From != nil {
		if tableSource, ok := stmt.From.TableRefs.Left.(*ast.TableSource); ok {
			if tableName, ok := tableSource.Source.(*ast.TableName); ok {
				plan.TableName = tableName.Name.L
			}
		}
	}

	// Parse SELECT columns
	if stmt.Fields != nil {
		for _, field := range stmt.Fields.Fields {
			// Handle SELECT *
			if field.WildCard != nil {
				plan.Columns = []string{"*"}
				break
			}
			// Handle column names
			if colName, ok := field.Expr.(*ast.ColumnNameExpr); ok {
				plan.Columns = append(plan.Columns, colName.Name.Name.L)
			}
		}
	}

	// If no columns specified, default to *
	if len(plan.Columns) == 0 {
		plan.Columns = []string{"*"}
	}

	// Parse WHERE clause
	if stmt.Where != nil {
		whereExpr, err := p.parseWhereExpr(stmt.Where)
		if err != nil {
			return nil, fmt.Errorf("failed to parse WHERE clause: %w", err)
		}
		plan.Where = whereExpr
	}

	// Parse LIMIT
	if stmt.Limit != nil {
		if stmt.Limit.Count != nil {
			// Extract value from Count expression
			if val := extractIntValue(stmt.Limit.Count); val >= 0 {
				plan.Limit = val
			}
		}
		if stmt.Limit.Offset != nil {
			// Extract value from Offset expression
			if val := extractIntValue(stmt.Limit.Offset); val >= 0 {
				plan.Offset = val
			}
		}
	}

	return plan, nil
}

// parseWhereExpr parses WHERE expression recursively
func (p *SQLParser) parseWhereExpr(expr ast.ExprNode) (*WhereCondition, error) {
	switch expr := expr.(type) {
	case *ast.BinaryOperationExpr:
		return p.parseBinaryOp(expr)
	case *ast.PatternInExpr:
		return p.parseInExpr(expr)
	case *ast.PatternLikeOrIlikeExpr:
		return p.parseLikeExpr(expr)
	case *ast.ParenthesesExpr:
		// Unwrap parentheses
		return p.parseWhereExpr(expr.Expr)
	default:
		return nil, fmt.Errorf("unsupported WHERE expression type: %T", expr)
	}
}

// parseBinaryOp parses binary operations (=, AND, OR, <, >, etc.)
func (p *SQLParser) parseBinaryOp(expr *ast.BinaryOperationExpr) (*WhereCondition, error) {
	switch expr.Op {
	case opcode.LogicAnd:
		// AND operation
		left, err := p.parseWhereExpr(expr.L)
		if err != nil {
			return nil, err
		}
		right, err := p.parseWhereExpr(expr.R)
		if err != nil {
			return nil, err
		}
		return &WhereCondition{
			Type:     ConditionTypeAnd,
			Operator: "AND",
			Children: []*WhereCondition{left, right},
		}, nil

	case opcode.LogicOr:
		// OR operation
		left, err := p.parseWhereExpr(expr.L)
		if err != nil {
			return nil, err
		}
		right, err := p.parseWhereExpr(expr.R)
		if err != nil {
			return nil, err
		}
		return &WhereCondition{
			Type:     ConditionTypeOr,
			Operator: "OR",
			Children: []*WhereCondition{left, right},
		}, nil

	case opcode.EQ, opcode.NE, opcode.LT, opcode.LE, opcode.GT, opcode.GE:
		// Simple comparison: key = 'value'
		colName, ok := expr.L.(*ast.ColumnNameExpr)
		if !ok {
			return nil, fmt.Errorf("left side of comparison must be column name, got %T", expr.L)
		}

		// Extract value from right side
		value := extractValue(expr.R)
		if value == nil {
			return nil, fmt.Errorf("right side of comparison must be value, got %T", expr.R)
		}

		return &WhereCondition{
			Type:     ConditionTypeSimple,
			Key:      colName.Name.Name.L,
			Value:    value,
			Operator: expr.Op.String(),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported binary operator: %s", expr.Op)
	}
}

// parseLikeExpr parses LIKE expression
func (p *SQLParser) parseLikeExpr(expr *ast.PatternLikeOrIlikeExpr) (*WhereCondition, error) {
	colName, ok := expr.Expr.(*ast.ColumnNameExpr)
	if !ok {
		return nil, fmt.Errorf("LIKE left side must be column name, got %T", expr.Expr)
	}

	// Extract pattern value
	patternStr := extractStringValue(expr.Pattern)
	// Extract prefix from 'prefix%' pattern
	prefix := strings.TrimRight(patternStr, "%")

	return &WhereCondition{
		Type:     ConditionTypeSimple,
		Key:      colName.Name.Name.L,
		IsLike:   true,
		Prefix:   prefix,
		Value:    patternStr,
		Operator: "LIKE",
	}, nil
}

// parseInExpr parses IN expression
func (p *SQLParser) parseInExpr(expr *ast.PatternInExpr) (*WhereCondition, error) {
	colName, ok := expr.Expr.(*ast.ColumnNameExpr)
	if !ok {
		return nil, fmt.Errorf("IN left side must be column name, got %T", expr.Expr)
	}

	// Parse list of values
	var values []interface{}
	for _, item := range expr.List {
		// Extract value using helper function
		val := extractValue(item)
		if val != nil {
			values = append(values, val)
		}
	}

	return &WhereCondition{
		Type:     ConditionTypeIn,
		Key:      colName.Name.Name.L,
		InValues: values,
		Operator: "IN",
	}, nil
}

// parseInsertStmt parses INSERT statement (placeholder for future implementation)
func (p *SQLParser) parseInsertStmt(stmt *ast.InsertStmt) (*QueryPlan, error) {
	return &QueryPlan{Type: QueryTypeInsert}, fmt.Errorf("INSERT not yet implemented")
}

// parseUpdateStmt parses UPDATE statement (placeholder for future implementation)
func (p *SQLParser) parseUpdateStmt(stmt *ast.UpdateStmt) (*QueryPlan, error) {
	return &QueryPlan{Type: QueryTypeUpdate}, fmt.Errorf("UPDATE not yet implemented")
}

// parseDeleteStmt parses DELETE statement (placeholder for future implementation)
func (p *SQLParser) parseDeleteStmt(stmt *ast.DeleteStmt) (*QueryPlan, error) {
	return &QueryPlan{Type: QueryTypeDelete}, fmt.Errorf("DELETE not yet implemented")
}

// Helper functions for value extraction

// extractValue extracts value from an expression node
func extractValue(expr ast.ExprNode) interface{} {
	switch e := expr.(type) {
	case *test_driver.ValueExpr:
		return e.GetValue()
	default:
		return nil
	}
}

// extractStringValue extracts string value from an expression node
func extractStringValue(expr ast.ExprNode) string {
	val := extractValue(expr)
	if str, ok := val.(string); ok {
		return str
	}
	return fmt.Sprintf("%v", val)
}

// extractIntValue extracts int64 value from an expression node
func extractIntValue(expr ast.ExprNode) int64 {
	val := extractValue(expr)
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case uint64:
		return int64(v)
	default:
		return -1
	}
}
