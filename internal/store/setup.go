package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ErrSetUp says the hub already has users, so the first-admin setup is over.
var ErrSetUp = errors.New("already set up")

// CreateFirstAdmin creates an admin only while there are no users at all.
// The users table is locked against inserts for the check and the insert,
// so requests racing on a fresh hub make exactly one admin.
func (s *Store) CreateFirstAdmin(ctx context.Context, username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(password) < 8 {
		return User{}, errors.New("username is required and the password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	// SHARE ROW EXCLUSIVE conflicts with itself and with INSERT: a second
	// setup, or `uptimeforge-hub admin`, waits until this one commits.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return User{}, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return User{}, err
	}
	if n > 0 {
		return User{}, ErrSetUp
	}
	now := time.Now()
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO users (username, password_hash, role, created) VALUES ($1, $2, $3, $4) RETURNING id`,
		username, string(hash), string(RoleAdmin), now).Scan(&id); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, Role: RoleAdmin, Created: now}, nil
}
