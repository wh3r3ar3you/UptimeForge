package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

type User struct {
	ID       int64     `json:"id"`
	Username string    `json:"username"`
	Role     Role      `json:"role"`
	Created  time.Time `json:"created"`
}

var ErrBadCredentials = errors.New("wrong username or password")

func (s *Store) UserCount(ctx context.Context) (n int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, role, created FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Created); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, username, password string, role Role) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(password) < 8 {
		return User{}, errors.New("username is required and the password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	now := time.Now()
	var id int64
	err = s.db.QueryRowContext(ctx, `INSERT INTO users (username, password_hash, role, created) VALUES ($1, $2, $3, $4) RETURNING id`,
		username, string(hash), string(role), now).Scan(&id)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == pgerrcode.UniqueViolation {
			return User{}, errors.New("user already exists")
		}
		return User{}, err
	}
	return User{ID: id, Username: username, Role: role, Created: now}, nil
}

func (s *Store) SetPassword(ctx context.Context, id int64, password string) error {
	if len(password) < 8 {
		return errors.New("the password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, string(hash), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// Authenticate checks credentials in constant-ish time even for unknown users.
func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, username, role, created, password_hash FROM users WHERE lower(username) = lower($1)`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.Role, &u.Created, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return User{}, ErrBadCredentials
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrBadCredentials
	}
	return u, nil
}

var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("uptimeforge"), bcrypt.DefaultCost)
	return h
})

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// CreateSession returns a random token; only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires, created) VALUES ($1, $2, $3, $4)`,
		hashToken(token), userID, now.Add(ttl), now)
	return token, err
}

func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.role, u.created FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires > $2`, hashToken(token), time.Now()).Scan(&u.ID, &u.Username, &u.Role, &u.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}
