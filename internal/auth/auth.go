// Package auth holds the authentication primitives: argon2id hashing,
// session tokens, CSRF tokens and the per-account throttling policy
// (plan §4). The service layer wires them to the store.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
)

// Params are the argon2id parameters: OWASP's 19 MiB / t=2 / p=1, which is
// gentle on a Raspberry Pi and still slow for an offline attack.
var Params = &argon2id.Params{Memory: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}

// MinPasswordLen is the admin password minimum (plan §4.1).
const MinPasswordLen = 10

var (
	ErrPasswordShort = fmt.Errorf("use at least %d characters", MinPasswordLen)
	ErrPINFormat     = errors.New("PIN must be digits only")
)

// Hash hashes a password or PIN with argon2id.
func Hash(secret string) (string, error) { return argon2id.CreateHash(secret, Params) }

// Verify reports whether secret matches hash. A malformed hash never matches.
func Verify(secret, hash string) bool {
	if hash == "" {
		return false
	}
	ok, err := argon2id.ComparePasswordAndHash(secret, hash)
	return err == nil && ok
}

// dummyHash lets unknown usernames cost the same time as real ones.
var dummyHash, _ = Hash("goldberry-timing-equalizer")

// BurnTime spends one hash verification, for unknown accounts.
func BurnTime(secret string) { Verify(secret, dummyHash) }

// CheckPassword validates an admin password.
func CheckPassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return ErrPasswordShort
	}
	return nil
}

// CheckPIN validates a kid PIN against the household PIN length.
func CheckPIN(pin string, length int) error {
	if len(pin) != length {
		return fmt.Errorf("PIN must be %d digits", length)
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return ErrPINFormat
		}
	}
	return nil
}

// NewToken returns a random 256-bit token, base64url without padding.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is what the sessions table stores, so a leaked DB holds no
// usable session cookies.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CSRFToken derives the token for a binding value (the session token, or a
// pre-session cookie for the login and setup forms).
func CSRFToken(key []byte, binding string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("csrf\x00" + binding))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyCSRF checks a submitted token in constant time.
func VerifyCSRF(key []byte, binding, token string) bool {
	if binding == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(CSRFToken(key, binding)), []byte(token)) == 1
}

// --- throttling --------------------------------------------------------------

const (
	// BackoffAfter is the number of free failures before delays start.
	BackoffAfter = 3
	// KidLockAfter hard-locks a kid account; only a parent can unlock it.
	KidLockAfter = 10
	baseDelay    = 5 * time.Second
	maxDelay     = 15 * time.Minute
)

// Throttle is the decision for one sign-in attempt.
type Throttle struct {
	Locked bool
	Wait   time.Duration // > 0: try again after this long
}

// Check decides whether an attempt may proceed now.
func Check(failures int, nextAllowed, locked *time.Time, now time.Time) Throttle {
	if locked != nil {
		return Throttle{Locked: true}
	}
	if nextAllowed != nil && now.Before(*nextAllowed) {
		return Throttle{Wait: nextAllowed.Sub(now)}
	}
	return Throttle{}
}

// Delay is the backoff after a given number of consecutive failures:
// none for the first BackoffAfter-1, then 5 s doubling up to 15 min.
func Delay(failures int) time.Duration {
	if failures < BackoffAfter {
		return 0
	}
	d := time.Duration(float64(baseDelay) * math.Pow(2, float64(failures-BackoffAfter)))
	if d > maxDelay || d <= 0 {
		return maxDelay
	}
	return d
}

// ShouldLock reports whether a kid reaching this many failures is locked.
// Admins never hard-lock.
func ShouldLock(isKid bool, failures int) bool { return isKid && failures >= KidLockAfter }
