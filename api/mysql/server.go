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
	"net"
	"sync"
	"sync/atomic"
	"time"

	"metaStore/internal/kvstore"
	"metaStore/pkg/config"
	"metaStore/pkg/log"

	"github.com/go-mysql-org/go-mysql/server"
	"go.uber.org/zap"
)

// Server MySQL-compatible protocol server
type Server struct {
	mu       sync.RWMutex
	store    kvstore.Store    // Underlying storage
	listener net.Listener     // Network listener
	handler  *MySQLHandler    // MySQL protocol handler

	// Configuration
	address      string
	authProvider *AuthProvider

	// Connection management
	connections sync.Map       // Active connections
	connCounter atomic.Uint64  // Connection counter

	// Lifecycle
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   atomic.Bool
}

// ServerConfig MySQL server configuration
type ServerConfig struct {
	Store     kvstore.Store  // Underlying storage (required)
	Address   string         // Listen address (e.g. ":3306")
	Username  string         // Auth username (default: "root")
	Password  string         // Auth password (default: "")
	Config    *config.Config // Full configuration object (optional)
}

// NewServer creates a new MySQL-compatible server
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if cfg.Address == "" {
		cfg.Address = ":3306"
	}
	if cfg.Username == "" {
		cfg.Username = "root"
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Server{
		store:   cfg.Store,
		address: cfg.Address,
		ctx:     ctx,
		cancel:  cancel,
	}

	// Create auth provider
	s.authProvider = NewAuthProvider(cfg.Username, cfg.Password)

	// Create MySQL handler
	s.handler = NewMySQLHandler(cfg.Store, s.authProvider)

	log.Info("MySQL server initialized",
		zap.String("address", cfg.Address),
		zap.String("component", "mysql"))

	return s, nil
}

// Start starts the MySQL server
func (s *Server) Start() error {
	if !s.running.CompareAndSwap(false, true) {
		return fmt.Errorf("server already running")
	}

	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		s.running.Store(false)
		return fmt.Errorf("failed to listen on %s: %v", s.address, err)
	}
	s.listener = listener

	log.Info("MySQL server starting",
		zap.String("address", s.address),
		zap.String("component", "mysql"))

	// Start accepting connections
	s.wg.Add(1)
	go s.acceptConnections()

	return nil
}

// Stop stops the MySQL server gracefully
func (s *Server) Stop() error {
	if !s.running.CompareAndSwap(true, false) {
		return nil // Already stopped
	}

	log.Info("MySQL server stopping", zap.String("component", "mysql"))

	// Cancel context to signal shutdown
	s.cancel()

	// Close listener to stop accepting new connections
	if s.listener != nil {
		s.listener.Close()
	}

	// Close all active connections
	s.connections.Range(func(key, value interface{}) bool {
		if conn, ok := value.(net.Conn); ok {
			conn.Close()
		}
		s.connections.Delete(key)
		return true
	})

	// Wait for all goroutines to finish
	s.wg.Wait()

	log.Info("MySQL server stopped", zap.String("component", "mysql"))
	return nil
}

// acceptConnections accepts incoming connections
func (s *Server) acceptConnections() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				// Server is shutting down
				return
			default:
				log.Error("Failed to accept connection",
					zap.Error(err),
					zap.String("component", "mysql"))
				continue
			}
		}

		connID := s.connCounter.Add(1)
		s.connections.Store(connID, conn)

		s.wg.Add(1)
		go s.handleConnection(conn, connID)
	}
}

// handleConnection handles a single MySQL connection
func (s *Server) handleConnection(conn net.Conn, connID uint64) {
	defer s.wg.Done()
	defer func() {
		conn.Close()
		s.connections.Delete(connID)
	}()

	log.Debug("New MySQL connection",
		zap.Uint64("conn_id", connID),
		zap.String("remote_addr", conn.RemoteAddr().String()),
		zap.String("component", "mysql"))

	// Create a dedicated handler for this connection (enables per-connection transactions)
	connHandler := NewMySQLHandler(s.store, s.authProvider)

	// Create MySQL connection handler
	mysqlConn, err := server.NewConn(
		conn,
		connHandler.user,
		connHandler.password,
		connHandler,
	)
	if err != nil {
		log.Error("Failed to create MySQL connection handler",
			zap.Error(err),
			zap.Uint64("conn_id", connID),
			zap.String("component", "mysql"))
		return
	}
	defer func() {
		// Clean up any uncommitted transaction on disconnect
		connHandler.removeTransaction()
	}()

	// Handle the connection (blocks until connection closes)
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			// Set read deadline to allow periodic context checks
			conn.SetReadDeadline(time.Now().Add(30 * time.Second))

			log.Debug("Waiting for MySQL command",
				zap.Uint64("conn_id", connID),
				zap.String("component", "mysql"))

			fmt.Printf("[DEBUG] Before HandleCommand (conn_id=%d)\n", connID)
			err := mysqlConn.HandleCommand()
			fmt.Printf("[DEBUG] After HandleCommand (conn_id=%d, err=%v)\n", connID, err)
			if err != nil {
				// Check if it's a timeout error
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Debug("Read timeout, continuing",
						zap.Uint64("conn_id", connID),
						zap.String("component", "mysql"))
					// Continue on timeout
					continue
				}
				// Connection closed or error
				if err.Error() != "EOF" {
					log.Info("Connection error",
						zap.Error(err),
						zap.Uint64("conn_id", connID),
						zap.String("component", "mysql"))
				}
				return
			}

			log.Debug("MySQL command handled successfully",
				zap.Uint64("conn_id", connID),
				zap.String("component", "mysql"))
		}
	}
}
