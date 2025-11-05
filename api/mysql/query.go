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

package mysql

import (
	"context"
	"fmt"
	"strings"

	"metaStore/internal/kvstore"
	"metaStore/pkg/log"
	"metaStore/api/mysql/parser"

	"github.com/go-mysql-org/go-mysql/mysql"
	"go.uber.org/zap"
)

// handleSelect handles SELECT queries with support for:
// - Multiple columns: SELECT key, value FROM kv
// - LIKE queries: WHERE key LIKE 'prefix%'
// - Exact match: WHERE key = 'exact'
func (h *MySQLHandler) handleSelect(ctx context.Context, query string) (*mysql.Result, error) {
	queryUpper := strings.ToUpper(query)

	// Handle special SELECT queries (including delimiter check)
	if strings.Contains(queryUpper, "@@") || strings.Contains(queryUpper, "VERSION()") || strings.Contains(queryUpper, "$$") {
		return h.handleSystemSelect(ctx, query)
	}

	// Handle constant SELECT queries (SELECT 1, SELECT 'hello', etc.)
	// These don't have FROM clause and just return constant values
	if !strings.Contains(queryUpper, " FROM ") {
		return h.handleConstantSelect(ctx, query)
	}

	// Try advanced SQL parser first for robust parsing
	var columns []string
	var whereClause *whereClause

	plan, parseErr := h.parseQuery(query)
	if parseErr == nil && plan != nil {
		// Successfully parsed with advanced parser
		columns = plan.Columns
		whereClause = h.convertWhereCondition(plan.Where)
		log.Debug("Using advanced SQL parser",
			zap.Strings("columns", columns),
			zap.String("component", "mysql"))
	} else {
		// Fallback to simple string parsing
		columns = h.parseSelectColumns(query)
		whereClause = h.parseWhereClause(query)
		log.Debug("Using simple parser (fallback)",
			zap.Strings("columns", columns),
			zap.String("component", "mysql"))
	}

	if len(columns) == 0 {
		columns = []string{"key", "value"} // Default to all columns
	}

	// Determine revision to read from (snapshot isolation)
	tx := h.getTransaction()
	var readRevision int64 = 0 // 0 means latest
	if tx != nil && tx.active {
		readRevision = tx.startRev // Read from transaction snapshot
		log.Debug("Reading from transaction snapshot",
			zap.Int64("snapshot_rev", readRevision),
			zap.String("component", "mysql"))
	}

	var resp *kvstore.RangeResponse
	var err error

	if whereClause == nil {
		// No WHERE clause - return all keys (with limit)
		resp, err = h.store.Range(ctx, "", "\x00", 100, readRevision)
	} else if whereClause.isLike {
		// LIKE query - use prefix matching
		prefix := whereClause.likePrefix
		endKey := h.getPrefixEndKey(prefix)
		resp, err = h.store.Range(ctx, prefix, endKey, 1000, readRevision)
	} else {
		// Exact match query
		resp, err = h.store.Range(ctx, whereClause.key, "", 1, readRevision)
	}

	if err != nil {
		log.Error("Failed to query keys",
			zap.Error(err),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR,
			fmt.Sprintf("failed to query: %v", err))
	}

	// Track reads in transaction for conflict detection
	if tx != nil && tx.active {
		tx.mu.Lock()
		for _, kv := range resp.Kvs {
			key := string(kv.Key)
			// Record the ModRevision of each key read
			tx.readSet[key] = kv.ModRevision
		}
		tx.mu.Unlock()
	}

	// Build result set with selected columns
	var rows [][]interface{}
	for _, kv := range resp.Kvs {
		row := make([]interface{}, len(columns))
		for i, col := range columns {
			switch col {
			case "key":
				row[i] = kv.Key
			case "value":
				row[i] = kv.Value
			default:
				row[i] = nil
			}
		}
		rows = append(rows, row)
	}

	resultset, err := mysql.BuildSimpleResultset(
		columns,
		rows,
		false,
	)
	if err != nil {
		return nil, err
	}

	return &mysql.Result{
		Status:       0,
		AffectedRows: uint64(len(rows)),
		Resultset:    resultset,
	}, nil
}

// handleSelectAll handles SELECT * queries (range query)
func (h *MySQLHandler) handleSelectAll(ctx context.Context) (*mysql.Result, error) {
	// Query all keys
	resp, err := h.store.Range(ctx, "", "\x00", 100, 0) // Limit to 100 keys
	if err != nil {
		log.Error("Failed to query all keys",
			zap.Error(err),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR,
			fmt.Sprintf("failed to query: %v", err))
	}

	// Build result set
	var rows [][]interface{}
	for _, kv := range resp.Kvs {
		rows = append(rows, []interface{}{kv.Key, kv.Value})
	}

	resultset, err := mysql.BuildSimpleResultset(
		[]string{"key", "value"},
		rows,
		false,
	)
	if err != nil {
		return nil, err
	}

	return &mysql.Result{
		Status:       0,
		AffectedRows: uint64(len(rows)),
		Resultset:    resultset,
	}, nil
}

// handleConstantSelect handles constant SELECT queries like SELECT 1, SELECT 'hello', etc.
func (h *MySQLHandler) handleConstantSelect(ctx context.Context, query string) (*mysql.Result, error) {
	// Extract the expression after SELECT
	queryUpper := strings.ToUpper(query)
	selectIdx := strings.Index(queryUpper, "SELECT")
	if selectIdx == -1 {
		return nil, mysql.NewError(mysql.ER_PARSE_ERROR, "invalid SELECT query")
	}

	// Get everything after SELECT
	expr := strings.TrimSpace(query[selectIdx+6:])

	// Simple parsing: just return the expression as a string value
	// This handles SELECT 1, SELECT 'hello', SELECT 1+1, etc.
	resultset, err := mysql.BuildSimpleResultset(
		[]string{expr}, // Column name is the expression itself
		[][]interface{}{{expr}}, // Single row with the expression value
		false,
	)
	if err != nil {
		return nil, err
	}

	return &mysql.Result{
		Status:    0,
		Resultset: resultset,
	}, nil
}

// handleSystemSelect handles system variable SELECT queries
func (h *MySQLHandler) handleSystemSelect(ctx context.Context, query string) (*mysql.Result, error) {
	queryUpper := strings.ToUpper(query)

	var columnName string
	var value interface{}

	if strings.Contains(queryUpper, "VERSION()") {
		columnName = "VERSION()"
		value = "8.0.0-MetaStore"
	} else if strings.Contains(queryUpper, "@@VERSION_COMMENT") {
		columnName = "@@version_comment"
		value = "MetaStore MySQL Compatible Server"
	} else if strings.Contains(queryUpper, "@@TX_ISOLATION") || strings.Contains(queryUpper, "@@TRANSACTION_ISOLATION") {
		columnName = "@@tx_isolation"
		value = "REPEATABLE-READ"
	} else if strings.Contains(queryUpper, "$$") {
		// Handle delimiter check query (SELECT $$)
		columnName = "$$"
		value = "$$"
	} else {
		// Generic system variable
		columnName = "value"
		value = ""
	}

	resultset, err := mysql.BuildSimpleResultset(
		[]string{columnName},
		[][]interface{}{{value}},
		false,
	)
	if err != nil {
		return nil, err
	}

	return &mysql.Result{
		Status:    0,
		Resultset: resultset,
	}, nil
}

// handleInsert handles INSERT queries
func (h *MySQLHandler) handleInsert(ctx context.Context, query string) (*mysql.Result, error) {
	// Parse INSERT query
	// Simple parser for: INSERT INTO kv (key, value) VALUES ('k1', 'v1')
	key, value, err := h.parseKeyValueFromInsert(query)
	if err != nil {
		return nil, mysql.NewError(mysql.ER_SYNTAX_ERROR, err.Error())
	}

	// Check if we're in a transaction
	tx := h.getTransaction()
	if tx != nil && tx.active {
		// Buffer operation in transaction
		tx.mu.Lock()
		tx.operations = append(tx.operations, TxOp{
			OpType: "PUT",
			Key:    key,
			Value:  value,
		})
		tx.mu.Unlock()

		log.Debug("Buffered INSERT in transaction",
			zap.String("key", key),
			zap.String("component", "mysql"))

		return &mysql.Result{
			Status:       0,
			AffectedRows: 1,
		}, nil
	}

	// Autocommit mode - execute immediately
	_, _, err = h.store.PutWithLease(ctx, key, value, 0)
	if err != nil {
		log.Error("Failed to insert key-value",
			zap.Error(err),
			zap.String("key", key),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR,
			fmt.Sprintf("failed to insert: %v", err))
	}

	return &mysql.Result{
		Status:       0,
		AffectedRows: 1,
	}, nil
}

// handleUpdate handles UPDATE queries
func (h *MySQLHandler) handleUpdate(ctx context.Context, query string) (*mysql.Result, error) {
	// Parse UPDATE query
	// Simple parser for: UPDATE kv SET value = 'v2' WHERE key = 'k1'
	key, value, err := h.parseKeyValueFromUpdate(query)
	if err != nil {
		return nil, mysql.NewError(mysql.ER_SYNTAX_ERROR, err.Error())
	}

	// Check if we're in a transaction
	tx := h.getTransaction()
	if tx != nil && tx.active {
		// Buffer operation in transaction (UPDATE is also a PUT)
		tx.mu.Lock()
		tx.operations = append(tx.operations, TxOp{
			OpType: "PUT",
			Key:    key,
			Value:  value,
		})
		tx.mu.Unlock()

		log.Debug("Buffered UPDATE in transaction",
			zap.String("key", key),
			zap.String("component", "mysql"))

		return &mysql.Result{
			Status:       0,
			AffectedRows: 1,
		}, nil
	}

	// Autocommit mode - execute immediately
	_, _, err = h.store.PutWithLease(ctx, key, value, 0)
	if err != nil {
		log.Error("Failed to update key-value",
			zap.Error(err),
			zap.String("key", key),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR,
			fmt.Sprintf("failed to update: %v", err))
	}

	return &mysql.Result{
		Status:       0,
		AffectedRows: 1,
	}, nil
}

// handleDelete handles DELETE queries
func (h *MySQLHandler) handleDelete(ctx context.Context, query string) (*mysql.Result, error) {
	// Parse DELETE query
	// Simple parser for: DELETE FROM kv WHERE key = 'k1'
	key := h.parseKeyFromDelete(query)
	if key == "" {
		return nil, mysql.NewError(mysql.ER_SYNTAX_ERROR, "invalid DELETE syntax")
	}

	// Check if we're in a transaction
	tx := h.getTransaction()
	if tx != nil && tx.active {
		// Buffer operation in transaction
		tx.mu.Lock()
		tx.operations = append(tx.operations, TxOp{
			OpType: "DELETE",
			Key:    key,
			Value:  "", // Not used for DELETE
		})
		tx.mu.Unlock()

		log.Debug("Buffered DELETE in transaction",
			zap.String("key", key),
			zap.String("component", "mysql"))

		return &mysql.Result{
			Status:       0,
			AffectedRows: 1,
		}, nil
	}

	// Autocommit mode - execute immediately
	deleted, _, _, err := h.store.DeleteRange(ctx, key, "")
	if err != nil {
		log.Error("Failed to delete key",
			zap.Error(err),
			zap.String("key", key),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR,
			fmt.Sprintf("failed to delete: %v", err))
	}

	return &mysql.Result{
		Status:       0,
		AffectedRows: uint64(deleted),
	}, nil
}

// handleShowDatabases handles SHOW DATABASES command
func (h *MySQLHandler) handleShowDatabases(ctx context.Context) (*mysql.Result, error) {
	// Return a virtual database list
	databases := [][]interface{}{
		{"metastore"},
	}

	resultset, err := mysql.BuildSimpleResultset(
		[]string{"Database"},
		databases,
		false,
	)
	if err != nil {
		return nil, err
	}

	return &mysql.Result{
		Status:    0,
		Resultset: resultset,
	}, nil
}

// handleShowTables handles SHOW TABLES command
func (h *MySQLHandler) handleShowTables(ctx context.Context) (*mysql.Result, error) {
	// Return a virtual table list (single "kv" table)
	tables := [][]interface{}{
		{"kv"},
	}

	resultset, err := mysql.BuildSimpleResultset(
		[]string{"Tables_in_metastore"},
		tables,
		false,
	)
	if err != nil {
		return nil, err
	}

	return &mysql.Result{
		Status:    0,
		Resultset: resultset,
	}, nil
}

// handleDescribe handles DESCRIBE/DESC command
func (h *MySQLHandler) handleDescribe(ctx context.Context, query string) (*mysql.Result, error) {
	// Return table schema for "kv" table
	fields := [][]interface{}{
		{"key", "varchar(1024)", "NO", "PRI", nil, ""},
		{"value", "blob", "YES", "", nil, ""},
	}

	resultset, err := mysql.BuildSimpleResultset(
		[]string{"Field", "Type", "Null", "Key", "Default", "Extra"},
		fields,
		false,
	)
	if err != nil {
		return nil, err
	}

	return &mysql.Result{
		Status:    0,
		Resultset: resultset,
	}, nil
}

// Simple SQL parsers (basic implementation)

func (h *MySQLHandler) parseKeyFromSelect(query string) string {
	queryUpper := strings.ToUpper(query)
	whereIdx := strings.Index(queryUpper, "WHERE")
	if whereIdx == -1 {
		return ""
	}

	wherePart := query[whereIdx+5:] // Skip "WHERE"
	keyIdx := strings.Index(strings.ToUpper(wherePart), "KEY")
	if keyIdx == -1 {
		return ""
	}

	// Find the value after = or IN
	eqIdx := strings.Index(wherePart[keyIdx:], "=")
	if eqIdx == -1 {
		return ""
	}

	valuePart := strings.TrimSpace(wherePart[keyIdx+eqIdx+1:])
	return h.extractQuotedValue(valuePart)
}

func (h *MySQLHandler) parseKeyValueFromInsert(query string) (string, string, error) {
	queryUpper := strings.ToUpper(query)
	valuesIdx := strings.Index(queryUpper, "VALUES")
	if valuesIdx == -1 {
		return "", "", fmt.Errorf("invalid INSERT syntax: missing VALUES")
	}

	valuesPart := strings.TrimSpace(query[valuesIdx+6:])
	// Extract values from (key, value) format
	startIdx := strings.Index(valuesPart, "(")
	endIdx := strings.Index(valuesPart, ")")
	if startIdx == -1 || endIdx == -1 {
		return "", "", fmt.Errorf("invalid INSERT syntax: missing parentheses")
	}

	values := strings.Split(valuesPart[startIdx+1:endIdx], ",")
	if len(values) < 2 {
		return "", "", fmt.Errorf("invalid INSERT syntax: expected (key, value)")
	}

	key := h.extractQuotedValue(strings.TrimSpace(values[0]))
	value := h.extractQuotedValue(strings.TrimSpace(values[1]))

	return key, value, nil
}

func (h *MySQLHandler) parseKeyValueFromUpdate(query string) (string, string, error) {
	queryUpper := strings.ToUpper(query)
	setIdx := strings.Index(queryUpper, "SET")
	whereIdx := strings.Index(queryUpper, "WHERE")

	if setIdx == -1 {
		return "", "", fmt.Errorf("invalid UPDATE syntax: missing SET")
	}
	if whereIdx == -1 {
		return "", "", fmt.Errorf("invalid UPDATE syntax: missing WHERE")
	}

	// Extract value from SET clause
	setPart := strings.TrimSpace(query[setIdx+3 : whereIdx])
	eqIdx := strings.Index(setPart, "=")
	if eqIdx == -1 {
		return "", "", fmt.Errorf("invalid UPDATE syntax: missing = in SET")
	}
	value := h.extractQuotedValue(strings.TrimSpace(setPart[eqIdx+1:]))

	// Extract key from WHERE clause
	key := h.parseKeyFromSelect(query)
	if key == "" {
		return "", "", fmt.Errorf("invalid UPDATE syntax: cannot parse key")
	}

	return key, value, nil
}

func (h *MySQLHandler) parseKeyFromDelete(query string) string {
	return h.parseKeyFromSelect(query)
}

func (h *MySQLHandler) extractQuotedValue(s string) string {
	s = strings.TrimSpace(s)
	// Remove quotes (single or double)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') ||
			(s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// whereClause represents a parsed WHERE clause
type whereClause struct {
	key        string // For exact match: WHERE key = 'value'
	isLike     bool   // True if using LIKE operator
	likePrefix string // Prefix for LIKE queries: 'prefix%' -> 'prefix'
	likePattern string // Full LIKE pattern
}

// parseSelectColumns parses the SELECT clause to determine which columns to return
// Supports: SELECT *, SELECT key, SELECT value, SELECT key,value, SELECT key, value
func (h *MySQLHandler) parseSelectColumns(query string) []string {
	queryUpper := strings.ToUpper(query)

	// Find SELECT and FROM positions
	selectIdx := strings.Index(queryUpper, "SELECT")
	fromIdx := strings.Index(queryUpper, "FROM")

	if selectIdx == -1 || fromIdx == -1 || fromIdx <= selectIdx {
		return []string{"key", "value"} // Default
	}

	// Extract column list between SELECT and FROM
	columnPart := strings.TrimSpace(query[selectIdx+6 : fromIdx])
	columnPartUpper := strings.ToUpper(columnPart)

	// Handle SELECT *
	if strings.TrimSpace(columnPartUpper) == "*" {
		return []string{"key", "value"}
	}

	// Parse comma-separated columns
	columns := strings.Split(columnPart, ",")
	var result []string

	for _, col := range columns {
		col = strings.TrimSpace(strings.ToLower(col))
		if col == "key" || col == "value" {
			result = append(result, col)
		}
	}

	if len(result) == 0 {
		return []string{"key", "value"} // Default if no valid columns
	}

	return result
}

// parseWhereClause parses the WHERE clause to extract key conditions
// Supports: WHERE key = 'exact', WHERE key LIKE 'prefix%'
// parseQuery uses advanced SQL parser for robust SQL parsing
// Falls back to simple string parsing if advanced parser fails
func (h *MySQLHandler) parseQuery(query string) (*parser.QueryPlan, error) {
	sqlParser := parser.NewSQLParser()
	plan, err := sqlParser.Parse(query)
	if err != nil {
		// Fall back to nil, caller will use simple parser
		log.Debug("Advanced parser failed, using fallback",
			zap.Error(err),
			zap.String("query", query),
			zap.String("component", "mysql"))
		return nil, err
	}
	return plan, nil
}

// convertWhereCondition converts parser WhereCondition to our internal whereClause
// Only handles simple cases for now; complex queries are left to future enhancement
func (h *MySQLHandler) convertWhereCondition(cond *parser.WhereCondition) *whereClause {
	if cond == nil {
		return nil
	}

	switch cond.Type {
	case parser.ConditionTypeSimple:
		if cond.IsLike {
			return &whereClause{
				isLike:      true,
				likePrefix:  cond.Prefix,
				likePattern: cond.Value.(string),
			}
		}
		// Simple equality: key = 'value'
		if cond.Operator == "eq" && cond.Key == "key" {
			if strVal, ok := cond.Value.(string); ok {
				return &whereClause{
					key:    strVal,
					isLike: false,
				}
			}
		}
	}

	// Complex conditions (AND/OR/IN) not yet supported by whereClause
	// Return nil to trigger fallback to simple parser or range query
	return nil
}

func (h *MySQLHandler) parseWhereClause(query string) *whereClause {
	queryUpper := strings.ToUpper(query)
	whereIdx := strings.Index(queryUpper, "WHERE")

	if whereIdx == -1 {
		return nil // No WHERE clause
	}

	wherePart := query[whereIdx+5:] // Skip "WHERE"
	wherePartUpper := strings.ToUpper(wherePart)

	// Check for LIKE operator
	likeIdx := strings.Index(wherePartUpper, " LIKE ")
	if likeIdx != -1 {
		// Extract LIKE pattern
		patternPart := strings.TrimSpace(wherePart[likeIdx+6:])
		pattern := h.extractQuotedValue(patternPart)

		// Convert SQL LIKE pattern to prefix
		// 'prefix%' -> 'prefix', 'prefix*' -> 'prefix'
		prefix := strings.TrimRight(pattern, "%*")

		return &whereClause{
			isLike:      true,
			likePrefix:  prefix,
			likePattern: pattern,
		}
	}

	// Check for = operator (exact match)
	keyIdx := strings.Index(wherePartUpper, "KEY")
	if keyIdx != -1 {
		eqIdx := strings.Index(wherePart[keyIdx:], "=")
		if eqIdx != -1 {
			valuePart := strings.TrimSpace(wherePart[keyIdx+eqIdx+1:])
			key := h.extractQuotedValue(valuePart)

			return &whereClause{
				key:    key,
				isLike: false,
			}
		}
	}

	return nil
}

// getPrefixEndKey returns the end key for a prefix range query
// For prefix "abc", returns "abd" (next key after all "abc*" keys)
func (h *MySQLHandler) getPrefixEndKey(prefix string) string {
	if prefix == "" {
		return "\x00"
	}

	// Increment the last byte to get the end of the range
	endKey := []byte(prefix)
	for i := len(endKey) - 1; i >= 0; i-- {
		if endKey[i] < 0xff {
			endKey[i]++
			return string(endKey)
		}
		// If byte is 0xff, set to 0x00 and continue to previous byte
		endKey[i] = 0x00
	}

	// If all bytes were 0xff, return a high value
	return prefix + "\xff"
}
