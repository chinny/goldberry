package store

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Household is the single install-wide row (one install = one family).
type Household struct {
	ID                string
	Name              string
	Currency          string
	Timezone          string
	PINLength         int
	AllowNegative     bool
	RequestExpiryDays int // 0 = requests never expire
	KidPickerLogin    bool
	CreatedAt         time.Time
}

// Location returns the household's time zone, falling back to UTC.
func (h Household) Location() *time.Location {
	if loc, err := time.LoadLocation(h.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

type Role string

const (
	RoleAdmin Role = "admin"
	RoleKid   Role = "kid"
)

type User struct {
	ID           string
	Role         Role
	Username     string
	DisplayName  string
	Avatar       string
	PasswordHash string // admins only
	PINHash      string // kids only
	Email        string // admins only, optional
	DisabledAt   *time.Time
	CreatedAt    time.Time
}

func (u User) IsAdmin() bool  { return u.Role == RoleAdmin }
func (u User) IsKid() bool    { return u.Role == RoleKid }
func (u User) Disabled() bool { return u.DisabledAt != nil }

// Initial is the first letter of the display name, for avatar tiles.
func (u User) Initial() string {
	for _, r := range u.DisplayName {
		return string(r)
	}
	return "?"
}

type Session struct {
	IDHash      string
	UserID      string
	DeviceLabel string
	Remember    bool
	CreatedAt   time.Time
	LastSeenAt  time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
}

// AuthAttempts tracks failed sign-ins per account (plan §4.1).
type AuthAttempts struct {
	UserID        string
	Failures      int
	WindowStart   *time.Time
	NextAllowedAt *time.Time
	LockedAt      *time.Time
}

type JarKind string

const (
	JarSpend  JarKind = "spend"
	JarSave   JarKind = "save"
	JarGive   JarKind = "give"
	JarCustom JarKind = "custom"
)

type Jar struct {
	ID         string
	KidID      string
	Name       string
	Kind       JarKind
	SortOrder  int
	ArchivedAt *time.Time
}

// JarBalance is a jar with its computed balance and pending holds.
type JarBalance struct {
	Jar
	Balance int64
	Held    int64
}

// Available is what can still be requested or debited: balance − holds.
func (b JarBalance) Available() int64 { return b.Balance - b.Held }

type SplitRule struct {
	KidID       string
	JarID       string
	BasisPoints int
}

type EntryKind string

const (
	KindDeposit     EntryKind = "deposit"
	KindWithdrawal  EntryKind = "withdrawal"
	KindAdjustment  EntryKind = "adjustment"
	KindAllowance   EntryKind = "allowance"
	KindInterest    EntryKind = "interest"
	KindTransferIn  EntryKind = "transfer_in"
	KindTransferOut EntryKind = "transfer_out"
	KindReversal    EntryKind = "reversal"
)

// LedgerEntry is one append-only money movement. Amount is signed minor units.
type LedgerEntry struct {
	ID             string
	KidID          string
	JarID          string
	Amount         int64
	Kind           EntryKind
	Comment        string
	PrivateNote    string
	ActorID        string // "" = system
	ReversesID     string
	TransferID     string
	BatchID        string
	RequestID      string
	ScheduleID     string
	IdempotencyKey string
	EffectiveAt    time.Time
	CreatedAt      time.Time

	// Filled by list queries.
	ActorName    string
	JarName      string
	ReversedByID string // id of the reversal that cancels this entry, if any
}

func (e LedgerEntry) Reversed() bool { return e.ReversedByID != "" }

type RequestStatus string

const (
	StatusPending   RequestStatus = "pending"
	StatusApproved  RequestStatus = "approved"
	StatusDenied    RequestStatus = "denied"
	StatusCancelled RequestStatus = "cancelled"
	StatusExpired   RequestStatus = "expired"
)

type WithdrawalRequest struct {
	ID             string
	KidID          string
	JarID          string
	Amount         int64
	ApprovedAmount int64 // set when approved; may be lower than Amount
	Reason         string
	GoalID         string
	Status         RequestStatus
	DecidedBy      string
	DecisionNote   string
	DecidedAt      *time.Time
	IdempotencyKey string
	CreatedAt      time.Time
	ExpiresAt      *time.Time // nil = never

	// Filled by list queries.
	KidName       string
	JarName       string
	DecidedByName string
}

type Notification struct {
	ID          string
	RecipientID string
	Kind        string
	Payload     json.RawMessage
	RequestID   string
	ReadAt      *time.Time
	CreatedAt   time.Time
}

type AuditEntry struct {
	ID        string
	ActorID   string
	Action    string
	TargetID  string
	Detail    json.RawMessage
	CreatedAt time.Time
}

// LedgerFilter narrows ListLedger. KidID is required.
type LedgerFilter struct {
	KidID string
	JarID string
	Limit int
}

// RequestFilter narrows ListRequests. Empty fields match everything.
type RequestFilter struct {
	KidID  string
	Status RequestStatus
	Limit  int
}

// NewID returns a UUIDv7 string: time-sortable and dialect-independent.
func NewID() string { return uuid.Must(uuid.NewV7()).String() }
