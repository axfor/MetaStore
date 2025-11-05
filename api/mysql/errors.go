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
	"fmt"

	"github.com/go-mysql-org/go-mysql/mysql"
)

// MySQL standard error codes
// These error codes are defined by MySQL protocol specification
// Reference: https://dev.mysql.com/doc/refman/en/server-error-reference.html

const (
	// Authentication errors
	ErrAccessDenied = mysql.ER_ACCESS_DENIED_ERROR // 1045

	// Syntax errors
	ErrSyntaxError = mysql.ER_SYNTAX_ERROR   // 1064
	ErrParseError  = mysql.ER_PARSE_ERROR    // 1064

	// Command errors
	ErrUnknownCommand = mysql.ER_UNKNOWN_COM_ERROR // 1047
	ErrNotSupported   = mysql.ER_NOT_SUPPORTED_YET // 1235

	// Data errors
	ErrKeyNotFound    = mysql.ER_KEY_NOT_FOUND     // 1032
	ErrDuplicateKey   = mysql.ER_DUP_KEY           // 1022
	ErrNoSuchTable    = mysql.ER_NO_SUCH_TABLE     // 1146
	ErrNoSuchDatabase = mysql.ER_BAD_DB_ERROR      // 1049

	// Transaction errors
	ErrLockWaitTimeout    = mysql.ER_LOCK_WAIT_TIMEOUT    // 1205
	ErrLockDeadlock       = mysql.ER_LOCK_DEADLOCK        // 1213
	ErrRollbackOnly       = mysql.ER_UNKNOWN_ERROR        // 1105

	// Generic errors
	ErrUnknownError   = mysql.ER_UNKNOWN_ERROR   // 1105
	ErrInternalError  = mysql.ER_INTERNAL_ERROR  // 1815
	ErrOutOfMemory    = mysql.ER_OUTOFMEMORY     // 1037
)

// NewMySQLError creates a new MySQL error with error code and message
func NewMySQLError(code uint16, message string) error {
	return mysql.NewError(code, message)
}

// NewAccessDeniedError creates access denied error
func NewAccessDeniedError(username, host string) error {
	message := fmt.Sprintf("Access denied for user '%s'@'%s'", username, host)
	return mysql.NewError(ErrAccessDenied, message)
}

// NewSyntaxError creates syntax error
func NewSyntaxError(message string) error {
	msg := fmt.Sprintf("You have an error in your SQL syntax: %s", message)
	return mysql.NewError(ErrSyntaxError, msg)
}

// NewNotSupportedError creates not supported error
func NewNotSupportedError(feature string) error {
	msg := fmt.Sprintf("Feature '%s' is not yet supported", feature)
	return mysql.NewError(ErrNotSupported, msg)
}

// NewUnknownCommandError creates unknown command error
func NewUnknownCommandError(command string) error {
	msg := fmt.Sprintf("Unknown command: %s", command)
	return mysql.NewError(ErrUnknownCommand, msg)
}

// NewNoSuchTableError creates no such table error
func NewNoSuchTableError(database, table string) error {
	msg := fmt.Sprintf("Table '%s.%s' doesn't exist", database, table)
	return mysql.NewError(ErrNoSuchTable, msg)
}

// NewInternalError creates internal error
func NewInternalError(message string) error {
	msg := fmt.Sprintf("Internal error: %s", message)
	return mysql.NewError(ErrInternalError, msg)
}
