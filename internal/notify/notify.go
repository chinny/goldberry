// Package notify creates in-app notifications (plan §8). Every notification
// is first a row in `notifications`, one per recipient, written in the same
// transaction as the change it reports. Email (Phase 3) will deliver copies
// through the Notifier seam; the rows are always the source of truth.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chinny/goldberry/internal/money"
	"github.com/chinny/goldberry/internal/store"
)

// Kinds of notification. The string is stored, so never rename one.
const (
	RequestCreated   = "request_created"   // → admins
	RequestApproved  = "request_approved"  // → kid
	RequestDenied    = "request_denied"    // → kid
	RequestCancelled = "request_cancelled" // → admins
	RequestExpired   = "request_expired"   // → kid and admins
	FundsAdded       = "funds_added"       // → kid
	FundsRemoved     = "funds_removed"     // → kid
	EntryReversed    = "entry_reversed"    // → kid
	SignInFailures   = "signin_failures"   // → admins
	KidLocked        = "kid_locked"        // → admins
	AllowancePosted  = "allowance_posted"  // → kid
	LockOverridden   = "lock_overridden"   // → admins
)

// Payload is the JSON stored with a notification. Only what the message needs.
type Payload struct {
	KidID          string `json:"kid_id,omitempty"`
	KidName        string `json:"kid_name,omitempty"`
	ActorName      string `json:"actor_name,omitempty"`
	Amount         int64  `json:"amount,omitempty"`
	ApprovedAmount int64  `json:"approved_amount,omitempty"`
	Jar            string `json:"jar,omitempty"`
	Text           string `json:"text,omitempty"` // reason or comment
	Note           string `json:"note,omitempty"` // decision note
	Failures       int    `json:"failures,omitempty"`
}

// Notifier delivers a copy of a notification outside the app (email in
// Phase 3; web push, ntfy or webhooks later).
type Notifier interface {
	Deliver(ctx context.Context, n store.Notification, to store.User) error
}

// Send writes one notification row per recipient inside tx.
func Send(ctx context.Context, tx store.Tx, now time.Time, recipients []string, kind, requestID string, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	for _, id := range recipients {
		if err := tx.InsertNotification(ctx, store.Notification{
			ID: store.NewID(), RecipientID: id, Kind: kind, Payload: body, RequestID: requestID, CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("notify %s: %w", kind, err)
		}
	}
	return nil
}

// Admins returns the IDs of every active admin.
func Admins(ctx context.Context, r store.Reader) ([]string, error) {
	users, err := r.ListUsers(ctx, store.RoleAdmin)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, u := range users {
		if !u.Disabled() {
			ids = append(ids, u.ID)
		}
	}
	return ids, nil
}

// Message is a rendered notification.
type Message struct {
	Title string
	Body  string
}

// Render turns a stored notification into words.
func Render(n store.Notification, cur money.Currency) Message {
	var p Payload
	_ = json.Unmarshal(n.Payload, &p)
	amt := cur.Format(p.Amount)
	switch n.Kind {
	case RequestCreated:
		return Message{fmt.Sprintf("%s asked for %s", p.KidName, amt), fmt.Sprintf("“%s” · from %s", p.Text, p.Jar)}
	case RequestApproved:
		title := fmt.Sprintf("%s approved %s", p.ActorName, cur.Format(p.ApprovedAmount))
		body := fmt.Sprintf("for “%s”", p.Text)
		if p.ApprovedAmount != 0 && p.ApprovedAmount != p.Amount {
			body = fmt.Sprintf("You asked for %s for “%s”", amt, p.Text)
		}
		return Message{title, withNote(body, p.Note)}
	case RequestDenied:
		return Message{fmt.Sprintf("%s said no to %s", p.ActorName, amt), withNote(fmt.Sprintf("for “%s”", p.Text), p.Note)}
	case RequestCancelled:
		return Message{fmt.Sprintf("%s cancelled a request for %s", p.KidName, amt), fmt.Sprintf("“%s”", p.Text)}
	case RequestExpired:
		return Message{fmt.Sprintf("A request for %s expired", amt), fmt.Sprintf("%s · “%s”. The money is available again.", p.KidName, p.Text)}
	case FundsAdded:
		return Message{fmt.Sprintf("%s added %s", p.ActorName, amt), withComment(p.Jar, p.Text)}
	case FundsRemoved:
		return Message{fmt.Sprintf("%s took out %s", p.ActorName, amt), withComment(p.Jar, p.Text)}
	case EntryReversed:
		return Message{fmt.Sprintf("%s undid %s", p.ActorName, cur.Signed(p.Amount)), withComment(p.Jar, p.Text)}
	case SignInFailures:
		return Message{fmt.Sprintf("Wrong PIN or password for %s", p.KidName), fmt.Sprintf("%d failed tries in a row. Sign-in is slowing down.", p.Failures)}
	case KidLocked:
		return Message{fmt.Sprintf("%s’s account is locked", p.KidName), fmt.Sprintf("%d wrong PINs. Unlock it from People.", p.Failures)}
	case AllowancePosted:
		return Message{fmt.Sprintf("Allowance: %s", cur.Signed(p.Amount)), withComment(p.Jar, p.Text)}
	case LockOverridden:
		what := fmt.Sprintf("%s broke their own lock on %s", p.KidName, p.Jar)
		if p.Note == "removed" {
			what = fmt.Sprintf("%s removed their own lock on %s", p.KidName, p.Jar)
		}
		body := "Reason they gave: “" + p.Text + "”"
		if p.Amount > 0 {
			body = "To move or ask for " + amt + " · " + body
		}
		return Message{what, body}
	default:
		return Message{n.Kind, ""}
	}
}

func withNote(body, note string) string {
	if note == "" {
		return body
	}
	return body + " · “" + note + "”"
}

func withComment(jar, text string) string {
	if text == "" {
		return jar
	}
	return jar + " · “" + text + "”"
}
