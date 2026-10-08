// Package store is the only package with SQL in it (plan §9.1). Store is
// defined in domain terms, not tables; handlers and services only see this
// interface. The contract suite in storetest is the real definition of the
// behaviour both engines must share.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a unique constraint (username, etc.) is hit.
	ErrConflict = errors.New("already exists")
)

// Store is a Goldberry database.
type Store interface {
	Reader

	// Tx runs fn in one write transaction. When lockKid is non-empty, writes
	// for that kid are serialized for the life of the transaction (SQLite:
	// BEGIN IMMEDIATE on a single-connection write pool; Postgres:
	// SELECT … FOR UPDATE on the kid's users row), so check-then-write is safe.
	Tx(ctx context.Context, lockKid string, fn func(Tx) error) error

	Dialect() string // "sqlite" or "postgres"
	Ping(ctx context.Context) error
	Close() error
}

// Tx is the read/write view inside a transaction.
type Tx interface {
	Reader
	Writer
}

// Reader holds every query.
type Reader interface {
	GetHousehold(ctx context.Context) (Household, error)
	GetSetting(ctx context.Context, key string) (string, error)

	CountAdmins(ctx context.Context, includeDisabled bool) (int, error)
	GetUser(ctx context.Context, id string) (User, error)
	GetUserByUsername(ctx context.Context, username string) (User, error)
	ListUsers(ctx context.Context, role Role) ([]User, error)

	GetSession(ctx context.Context, idHash string) (Session, error)
	ListSessions(ctx context.Context, userID string, now time.Time) ([]Session, error)
	GetAuthAttempts(ctx context.Context, userID string) (AuthAttempts, error)

	ListJars(ctx context.Context, kidID string) ([]Jar, error)
	GetJar(ctx context.Context, id string) (Jar, error)
	JarBalances(ctx context.Context, kidID string) ([]JarBalance, error)
	ListSplitRules(ctx context.Context, kidID string) ([]SplitRule, error)

	GetLedgerEntry(ctx context.Context, id string) (LedgerEntry, error)
	GetLedgerEntryByKey(ctx context.Context, key string) (LedgerEntry, error)
	ListLedger(ctx context.Context, f LedgerFilter) ([]LedgerEntry, error)

	GetRequest(ctx context.Context, id string) (WithdrawalRequest, error)
	GetRequestByKey(ctx context.Context, key string) (WithdrawalRequest, error)
	ListRequests(ctx context.Context, f RequestFilter) ([]WithdrawalRequest, error)
	CountPendingRequests(ctx context.Context, kidID string) (int, error)

	ListNotifications(ctx context.Context, recipientID string, limit int) ([]Notification, error)
	CountUnread(ctx context.Context, recipientID string) (int, error)

	ListAudit(ctx context.Context, targetID string, limit int) ([]AuditEntry, error)
}

// Writer holds every write. There is deliberately no way to update or delete
// a ledger entry.
type Writer interface {
	CreateHousehold(ctx context.Context, h Household) error
	UpdateHousehold(ctx context.Context, h Household) error
	PutSetting(ctx context.Context, key, value string, secret bool) error

	CreateUser(ctx context.Context, u User) error
	UpdateUser(ctx context.Context, u User) error

	CreateSession(ctx context.Context, s Session) error
	TouchSession(ctx context.Context, idHash string, lastSeen, expires time.Time) error
	RevokeSession(ctx context.Context, idHash string, at time.Time) error
	RevokeUserSessions(ctx context.Context, userID string, at time.Time) error
	PutAuthAttempts(ctx context.Context, a AuthAttempts) error
	ClearAuthAttempts(ctx context.Context, userID string) error

	CreateJar(ctx context.Context, j Jar) error
	PutSplitRule(ctx context.Context, r SplitRule) error

	// InsertLedgerEntry appends an entry. It reports false, with no error,
	// when the idempotency key was already used.
	InsertLedgerEntry(ctx context.Context, e LedgerEntry) (bool, error)

	// InsertRequest reports false, with no error, on a reused idempotency key.
	InsertRequest(ctx context.Context, r WithdrawalRequest) (bool, error)
	// DecideRequest moves a pending request to a final status. It reports
	// false when the request was no longer pending: the first decision wins.
	DecideRequest(ctx context.Context, r WithdrawalRequest) (bool, error)
	// ExpireRequests marks pending requests past expires_at as expired and
	// returns them.
	ExpireRequests(ctx context.Context, now time.Time) ([]WithdrawalRequest, error)

	InsertNotification(ctx context.Context, n Notification) error
	MarkNotificationsRead(ctx context.Context, recipientID string, ids []string, at time.Time) error

	InsertAudit(ctx context.Context, a AuditEntry) error
}
