// Package auth implements the single-admin login of ADR-0006: bcrypt password
// hashes and server-side sessions. A session token is 256 random bits handed
// to the browser once; only its SHA-256 hash is stored.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"infrascope/api/internal/backend"
)

// User is an account that can log in.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// Session is an authenticated browser session.
type Session struct {
	Username  string
	ExpiresAt time.Time
}

// Store persists users and sessions. Methods wrap backend.ErrNotFound for
// missing rows, and backend.ErrUnavailable or backend.ErrTimeout when the
// database cannot answer.
type Store interface {
	UserByName(ctx context.Context, username string) (User, error)
	CreateUser(ctx context.Context, username, passwordHash string) error
	// ReplacePassword updates the hash and deletes the user's sessions in
	// one transaction.
	ReplacePassword(ctx context.Context, userID int64, passwordHash string) error
	// DeleteUsersExcept deletes every user but username, with their
	// sessions, and returns the names it deleted.
	DeleteUsersExcept(ctx context.Context, username string) ([]string, error)
	CreateSession(ctx context.Context, tokenHash []byte, userID int64, expiresAt time.Time) error
	// Session returns the session for tokenHash if it is unexpired at now.
	Session(ctx context.Context, tokenHash []byte, now time.Time) (Session, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error
}

var (
	// ErrInvalidCredentials is returned for a wrong username or password. It
	// deliberately does not say which of the two was wrong.
	ErrInvalidCredentials = errors.New("invalid username or password")

	// ErrInvalidSession is returned for a missing, malformed, expired or
	// revoked session token.
	ErrInvalidSession = errors.New("invalid or expired session")
)

// MaxPasswordBytes is the longest password bcrypt accepts.
const MaxPasswordBytes = 72

const tokenBytes = 32

// Bootstrap outcomes.
const (
	AdminCreated         = "created"
	AdminPasswordChanged = "password_changed"
	AdminUnchanged       = "unchanged"
)

// Service logs users in and validates their sessions.
type Service struct {
	store     Store
	ttl       time.Duration
	cost      int
	now       func() time.Time
	dummyHash []byte
}

// NewService returns a Service whose sessions last ttl. cost is the bcrypt
// work factor: production uses 12, tests use bcrypt.MinCost.
func NewService(store Store, ttl time.Duration, cost int) (*Service, error) {
	// Logins for unknown users are compared against this hash, so they take
	// as long as logins for real users and do not reveal which names exist.
	dummy, err := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), cost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt: %w", err)
	}
	return &Service{store: store, ttl: ttl, cost: cost, now: time.Now, dummyHash: dummy}, nil
}

// BootstrapResult says what Bootstrap did.
type BootstrapResult struct {
	// Outcome is AdminCreated, AdminPasswordChanged or AdminUnchanged.
	Outcome string
	// Removed names the accounts deleted because they were not the
	// configured admin, for example after INFRASCOPE_ADMIN_USER changed.
	Removed []string
}

// Bootstrap makes the accounts match the environment (ADR-0006): the
// configured admin is the only account. It creates the admin on first start
// and, when the configured password has changed, replaces the hash and
// revokes every session. It then deletes any other account with its
// sessions, so renaming the admin cannot leave the previous login working.
func (s *Service) Bootstrap(ctx context.Context, username, password string) (BootstrapResult, error) {
	outcome, err := s.bootstrapAdmin(ctx, username, password)
	if err != nil {
		return BootstrapResult{}, err
	}
	removed, err := s.store.DeleteUsersExcept(ctx, username)
	if err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{Outcome: outcome, Removed: removed}, nil
}

func (s *Service) bootstrapAdmin(ctx context.Context, username, password string) (string, error) {
	user, err := s.store.UserByName(ctx, username)
	switch {
	case errors.Is(err, backend.ErrNotFound):
		hash, err := s.hash(password)
		if err != nil {
			return "", err
		}
		return AdminCreated, s.store.CreateUser(ctx, username, hash)
	case err != nil:
		return "", err
	case bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil:
		return AdminUnchanged, nil
	}
	hash, err := s.hash(password)
	if err != nil {
		return "", err
	}
	return AdminPasswordChanged, s.store.ReplacePassword(ctx, user.ID, hash)
}

// Login verifies the credentials and starts a session. It returns the token
// for the session cookie; the token itself is never stored.
func (s *Service) Login(ctx context.Context, username, password string) (string, time.Time, error) {
	user, err := s.store.UserByName(ctx, username)
	switch {
	case errors.Is(err, backend.ErrNotFound):
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return "", time.Time{}, ErrInvalidCredentials
	case err != nil:
		return "", time.Time{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return "", time.Time{}, ErrInvalidCredentials
	}

	now := s.now()
	// Pruning on the infrequent login path keeps the sessions table small
	// without a background job.
	if err := s.store.DeleteExpiredSessions(ctx, now); err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := now.Add(s.ttl)
	if err := s.store.CreateSession(ctx, hashToken(token), user.ID, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// Authenticate returns the session that token belongs to, or
// ErrInvalidSession.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	if !wellFormed(token) {
		return Session{}, ErrInvalidSession
	}
	session, err := s.store.Session(ctx, hashToken(token), s.now())
	if errors.Is(err, backend.ErrNotFound) {
		return Session{}, ErrInvalidSession
	}
	return session, err
}

// Logout ends the session that token belongs to. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, token string) error {
	if !wellFormed(token) {
		return nil
	}
	return s.store.DeleteSession(ctx, hashToken(token))
}

func (s *Service) hash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(hash), nil
}

func wellFormed(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == tokenBytes
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
