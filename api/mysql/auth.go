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
	"crypto/sha1"
	"fmt"
	"sync"

	"metaStore/pkg/log"

	"go.uber.org/zap"
)

// AuthProvider handles MySQL authentication
type AuthProvider struct {
	mu       sync.RWMutex
	username string
	password string
	users    map[string]string // username -> password hash
}

// NewAuthProvider creates a new authentication provider
func NewAuthProvider(username, password string) *AuthProvider {
	if username == "" {
		username = "root"
	}

	ap := &AuthProvider{
		username: username,
		password: password,
		users:    make(map[string]string),
	}

	// Add default user
	ap.users[username] = password

	log.Info("MySQL auth provider initialized",
		zap.String("username", username),
		zap.String("component", "mysql"))

	return ap
}

// CheckAuth verifies username and password
func (ap *AuthProvider) CheckAuth(username, password string) bool {
	ap.mu.RLock()
	defer ap.mu.RUnlock()

	expectedPass, exists := ap.users[username]
	if !exists {
		log.Warn("Authentication failed: user not found",
			zap.String("username", username),
			zap.String("component", "mysql"))
		return false
	}

	// For empty password, allow any password
	if expectedPass == "" {
		return true
	}

	// Check password match
	if password != expectedPass {
		log.Warn("Authentication failed: password mismatch",
			zap.String("username", username),
			zap.String("component", "mysql"))
		return false
	}

	log.Debug("Authentication successful",
		zap.String("username", username),
		zap.String("component", "mysql"))
	return true
}

// AddUser adds a new user
func (ap *AuthProvider) AddUser(username, password string) error {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	if _, exists := ap.users[username]; exists {
		return fmt.Errorf("user %s already exists", username)
	}

	ap.users[username] = password
	log.Info("User added",
		zap.String("username", username),
		zap.String("component", "mysql"))
	return nil
}

// RemoveUser removes a user
func (ap *AuthProvider) RemoveUser(username string) error {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	if _, exists := ap.users[username]; !exists {
		return fmt.Errorf("user %s not found", username)
	}

	delete(ap.users, username)
	log.Info("User removed",
		zap.String("username", username),
		zap.String("component", "mysql"))
	return nil
}

// UpdatePassword updates user password
func (ap *AuthProvider) UpdatePassword(username, newPassword string) error {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	if _, exists := ap.users[username]; !exists {
		return fmt.Errorf("user %s not found", username)
	}

	ap.users[username] = newPassword
	log.Info("Password updated",
		zap.String("username", username),
		zap.String("component", "mysql"))
	return nil
}

// HashPassword creates a SHA1 hash of the password (MySQL native authentication)
func HashPassword(password string) string {
	if password == "" {
		return ""
	}

	hash := sha1.New()
	hash.Write([]byte(password))
	s1 := hash.Sum(nil)

	hash.Reset()
	hash.Write(s1)
	s2 := hash.Sum(nil)

	return fmt.Sprintf("%X", s2)
}
