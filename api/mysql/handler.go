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
	"sync"

	"metaStore/internal/kvstore"
	"metaStore/pkg/log"

	"github.com/go-mysql-org/go-mysql/mysql"
	"go.uber.org/zap"
)

// MySQLHandler implements MySQL protocol handler interface
// Each handler instance is specific to one MySQL connection
type MySQLHandler struct {
	store        kvstore.Store
	authProvider *AuthProvider
	user         string
	password     string

	// Transaction support (per-connection)
	txMu         sync.Mutex
	transaction  *Transaction // Current transaction for this connection
}

// Transaction represents an active transaction
type Transaction struct {
	mu          sync.Mutex
	active      bool              // Transaction active flag
	startRev    int64             // Snapshot revision at BEGIN
	operations  []TxOp            // Buffered operations
	readSet     map[string]int64  // Key -> ModRevision for conflict detection
}

// TxOp represents a transaction operation
type TxOp struct {
	OpType string // PUT, DELETE
	Key    string
	Value  string
}

// NewMySQLHandler creates a new MySQL protocol handler for a connection
func NewMySQLHandler(store kvstore.Store, authProvider *AuthProvider) *MySQLHandler {
	return &MySQLHandler{
		store:        store,
		authProvider: authProvider,
		user:         authProvider.username,
		password:     authProvider.password,
		transaction:  nil, // No active transaction initially
	}
}

// UseDB handles USE database command
func (h *MySQLHandler) UseDB(dbName string) error {
	fmt.Printf("[DEBUG] UseDB called: dbName=%s\n", dbName)
	log.Debug("USE database command",
		zap.String("database", dbName),
		zap.String("component", "mysql"))
	// Accept any database name for compatibility
	return nil
}

// HandleQuery handles SQL query commands
func (h *MySQLHandler) HandleQuery(query string) (*mysql.Result, error) {
	ctx := context.Background()
	query = strings.TrimSpace(query)
	queryUpper := strings.ToUpper(query)

	log.Info("Handling query",
		zap.String("query", query),
		zap.String("query_upper", queryUpper),
		zap.String("component", "mysql"))

	// Parse and execute query
	switch {
	case strings.HasPrefix(queryUpper, "SELECT"):
		return h.handleSelect(ctx, query)
	case strings.HasPrefix(queryUpper, "INSERT"):
		return h.handleInsert(ctx, query)
	case strings.HasPrefix(queryUpper, "UPDATE"):
		return h.handleUpdate(ctx, query)
	case strings.HasPrefix(queryUpper, "DELETE"):
		return h.handleDelete(ctx, query)
	case strings.HasPrefix(queryUpper, "USE"):
		// Handle USE database command (accept for compatibility)
		return &mysql.Result{Status: 0, AffectedRows: 0}, nil
	case strings.HasPrefix(queryUpper, "BEGIN") || queryUpper == "START TRANSACTION":
		return h.handleBegin(ctx)
	case strings.HasPrefix(queryUpper, "COMMIT"):
		return h.handleCommit(ctx)
	case strings.HasPrefix(queryUpper, "ROLLBACK"):
		return h.handleRollback(ctx)
	case strings.HasPrefix(queryUpper, "SHOW DATABASES"):
		return h.handleShowDatabases(ctx)
	case strings.HasPrefix(queryUpper, "SHOW TABLES"):
		return h.handleShowTables(ctx)
	case strings.HasPrefix(queryUpper, "DESCRIBE") || strings.HasPrefix(queryUpper, "DESC"):
		return h.handleDescribe(ctx, query)
	case queryUpper == "PING" || strings.HasPrefix(queryUpper, "SELECT 1"):
		return h.handlePing(ctx)
	case strings.HasPrefix(queryUpper, "SET"):
		// Accept SET commands for compatibility (usually SET autocommit, etc.)
		return &mysql.Result{
			Status:       0,
			AffectedRows: 0,
		}, nil
	default:
		log.Warn("Unsupported SQL command",
			zap.String("query", query),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_UNKNOWN_COM_ERROR,
			fmt.Sprintf("unsupported SQL command: %s", query))
	}
}

// HandleFieldList handles field list command
func (h *MySQLHandler) HandleFieldList(table string, fieldWildcard string) ([]*mysql.Field, error) {
	fmt.Printf("[DEBUG] HandleFieldList called: table=%s wildcard=%s\n", table, fieldWildcard)
	log.Debug("Field list command",
		zap.String("table", table),
		zap.String("wildcard", fieldWildcard),
		zap.String("component", "mysql"))

	// Return a simple schema for KV store
	// Using UTF-8 charset (33)
	fields := []*mysql.Field{
		{
			Name:         []byte("key"),
			Type:         mysql.MYSQL_TYPE_VARCHAR,
			Charset:      33, // UTF-8
			ColumnLength: 1024,
		},
		{
			Name:         []byte("value"),
			Type:         mysql.MYSQL_TYPE_BLOB,
			Charset:      63, // Binary
			ColumnLength: 65535,
		},
	}

	return fields, nil
}

// HandleStmtPrepare handles prepared statement preparation
func (h *MySQLHandler) HandleStmtPrepare(query string) (params int, columns int, ctx interface{}, err error) {
	log.Debug("Prepare statement",
		zap.String("query", query),
		zap.String("component", "mysql"))

	// For simplicity, return unsupported error
	// Full implementation would parse query and return parameter/column counts
	return 0, 0, nil, mysql.NewError(mysql.ER_UNKNOWN_COM_ERROR,
		"prepared statements not yet supported")
}

// HandleStmtExecute handles prepared statement execution
func (h *MySQLHandler) HandleStmtExecute(ctx interface{}, query string, args []interface{}) (*mysql.Result, error) {
	log.Debug("Execute statement",
		zap.String("query", query),
		zap.String("component", "mysql"))

	return nil, mysql.NewError(mysql.ER_UNKNOWN_COM_ERROR,
		"prepared statements not yet supported")
}

// HandleStmtClose handles prepared statement close
func (h *MySQLHandler) HandleStmtClose(ctx interface{}) error {
	return nil
}

// HandleOtherCommand handles other MySQL commands
func (h *MySQLHandler) HandleOtherCommand(cmd byte, data []byte) error {
	fmt.Printf("[DEBUG] HandleOtherCommand called: cmd=%d name=%s data_len=%d\n", cmd, getCommandName(cmd), len(data))
	log.Info("Other command received",
		zap.Uint8("cmd", cmd),
		zap.String("cmd_name", getCommandName(cmd)),
		zap.Int("data_len", len(data)),
		zap.String("component", "mysql"))

	switch cmd {
	case mysql.COM_QUIT:
		fmt.Println("[DEBUG] COM_QUIT received")
		log.Info("COM_QUIT received", zap.String("component", "mysql"))
		return nil
	case mysql.COM_PING:
		fmt.Println("[DEBUG] COM_PING received")
		log.Info("COM_PING received", zap.String("component", "mysql"))
		return nil
	case mysql.COM_INIT_DB:
		fmt.Println("[DEBUG] COM_INIT_DB received")
		// Already handled by UseDB
		log.Info("COM_INIT_DB received", zap.String("component", "mysql"))
		return nil
	case mysql.COM_SET_OPTION:
		fmt.Println("[DEBUG] COM_SET_OPTION received")
		log.Debug("COM_SET_OPTION received", zap.String("component", "mysql"))
		// Accept SET OPTION command (used by mysql client for multi-statement settings)
		return nil
	default:
		fmt.Printf("[DEBUG] Unknown command: %d\n", cmd)
		log.Warn("Unknown command received",
			zap.Uint8("cmd", cmd),
			zap.String("component", "mysql"))
		return mysql.NewError(mysql.ER_UNKNOWN_COM_ERROR,
			fmt.Sprintf("command %d not supported", cmd))
	}
}

func getCommandName(cmd byte) string {
	names := map[byte]string{
		mysql.COM_QUIT:       "COM_QUIT",
		mysql.COM_INIT_DB:    "COM_INIT_DB",
		mysql.COM_QUERY:      "COM_QUERY",
		mysql.COM_PING:       "COM_PING",
		mysql.COM_SET_OPTION: "COM_SET_OPTION",
		0x16:                 "COM_STMT_PREPARE",
		0x17:                 "COM_STMT_EXECUTE",
		0x19:                 "COM_STMT_CLOSE",
		0x1a:                 "COM_STMT_RESET",
		0x1c:                 "COM_STMT_FETCH",
	}
	if name, ok := names[cmd]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN(%d)", cmd)
}

// Transaction management (per-connection methods)

// getTransaction returns the current transaction for this connection
func (h *MySQLHandler) getTransaction() *Transaction {
	h.txMu.Lock()
	defer h.txMu.Unlock()
	return h.transaction
}

// createTransaction creates a new transaction for this connection
func (h *MySQLHandler) createTransaction() *Transaction {
	h.txMu.Lock()
	defer h.txMu.Unlock()

	tx := &Transaction{
		active:     true,
		operations: make([]TxOp, 0),
		readSet:    make(map[string]int64),
		startRev:   h.store.CurrentRevision(),
	}
	h.transaction = tx
	return tx
}

// removeTransaction removes the current transaction for this connection
func (h *MySQLHandler) removeTransaction() {
	h.txMu.Lock()
	defer h.txMu.Unlock()
	h.transaction = nil
}

// Helper methods

// handleBegin starts a new transaction with snapshot isolation
func (h *MySQLHandler) handleBegin(ctx context.Context) (*mysql.Result, error) {
	tx := h.getTransaction()
	if tx != nil && tx.active {
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR,
			"transaction already active")
	}

	// Create new transaction with current revision as snapshot
	h.createTransaction()

	log.Debug("BEGIN transaction",
		zap.Int64("snapshot_rev", h.transaction.startRev),
		zap.String("component", "mysql"))

	return &mysql.Result{
		Status:       0,
		AffectedRows: 0,
	}, nil
}

// handleCommit commits the transaction using optimistic concurrency control
func (h *MySQLHandler) handleCommit(ctx context.Context) (*mysql.Result, error) {
	tx := h.getTransaction()
	if tx == nil || !tx.active {
		// No active transaction - no-op
		return &mysql.Result{Status: 0, AffectedRows: 0}, nil
	}

	tx.mu.Lock()
	defer tx.mu.Unlock()

	log.Debug("COMMIT transaction",
		zap.Int("operations", len(tx.operations)),
		zap.Int("read_set_size", len(tx.readSet)),
		zap.String("component", "mysql"))

	// If no operations, just clean up
	if len(tx.operations) == 0 {
		h.removeTransaction()
		return &mysql.Result{Status: 0, AffectedRows: 0}, nil
	}

	// Build comparison conditions for conflict detection (read set validation)
	cmps := make([]kvstore.Compare, 0, len(tx.readSet))
	for key, expectedRev := range tx.readSet {
		cmps = append(cmps, kvstore.Compare{
			Target: kvstore.CompareMod,
			Result: kvstore.CompareEqual,
			Key:    []byte(key),
			TargetUnion: kvstore.CompareUnion{
				ModRevision: expectedRev,
			},
		})
	}

	// Build operations to apply
	thenOps := make([]kvstore.Op, 0, len(tx.operations))
	for _, op := range tx.operations {
		switch op.OpType {
		case "PUT":
			thenOps = append(thenOps, kvstore.Op{
				Type:  kvstore.OpPut,
				Key:   []byte(op.Key),
				Value: []byte(op.Value),
			})
		case "DELETE":
			thenOps = append(thenOps, kvstore.Op{
				Type: kvstore.OpDelete,
				Key:  []byte(op.Key),
			})
		}
	}

	// Execute transaction atomically with conflict detection
	txnResp, err := h.store.Txn(ctx, cmps, thenOps, nil)
	if err != nil {
		h.removeTransaction()
		log.Error("Transaction commit failed",
			zap.Error(err),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR,
			fmt.Sprintf("transaction commit failed: %v", err))
	}

	// Check if transaction succeeded (all comparisons passed)
	if !txnResp.Succeeded {
		h.removeTransaction()
		log.Warn("Transaction conflict detected",
			zap.Int("read_set_size", len(tx.readSet)),
			zap.String("component", "mysql"))
		return nil, mysql.NewError(mysql.ER_LOCK_DEADLOCK,
			"transaction conflict: data was modified by another transaction")
	}

	// Success - clean up transaction
	affectedRows := uint64(len(tx.operations))
	h.removeTransaction()

	log.Debug("Transaction committed successfully",
		zap.Uint64("affected_rows", affectedRows),
		zap.Int64("new_revision", txnResp.Revision),
		zap.String("component", "mysql"))

	return &mysql.Result{
		Status:       0,
		AffectedRows: affectedRows,
	}, nil
}

// handleRollback rolls back the transaction (discards buffered operations)
func (h *MySQLHandler) handleRollback(ctx context.Context) (*mysql.Result, error) {
	tx := h.getTransaction()
	if tx == nil || !tx.active {
		// No active transaction - no-op
		return &mysql.Result{Status: 0, AffectedRows: 0}, nil
	}

	tx.mu.Lock()
	operations := len(tx.operations)
	tx.mu.Unlock()

	// Simply remove transaction (discards all buffered operations)
	h.removeTransaction()

	log.Debug("ROLLBACK transaction",
		zap.Int("discarded_operations", operations),
		zap.String("component", "mysql"))

	return &mysql.Result{
		Status:       0,
		AffectedRows: 0,
	}, nil
}

func (h *MySQLHandler) handlePing(ctx context.Context) (*mysql.Result, error) {
	// Return a simple successful result
	resultset, _ := mysql.BuildSimpleResultset(
		[]string{"result"},
		[][]interface{}{{1}},
		false,
	)
	return &mysql.Result{
		Status:    0,
		Resultset: resultset,
	}, nil
}
