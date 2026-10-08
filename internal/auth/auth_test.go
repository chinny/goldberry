package auth

import (
	"testing"
	"time"
)

func TestHashVerify(t *testing.T) {
	h, err := Hash("1234")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("1234", h) || Verify("1235", h) || Verify("1234", "") || Verify("1234", "garbage") {
		t.Fatal("verify mismatch")
	}
}

func TestChecks(t *testing.T) {
	if CheckPassword("short") == nil || CheckPassword("long enough!") != nil {
		t.Error("password length check")
	}
	if CheckPIN("1234", 4) != nil || CheckPIN("12a4", 4) == nil || CheckPIN("123", 4) == nil || CheckPIN("123456", 6) != nil {
		t.Error("pin check")
	}
}

func TestTokens(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q %q", a, b)
	}
	if HashToken(a) == a || len(HashToken(a)) != 64 {
		t.Fatal("hash token")
	}
	key := []byte("k")
	tok := CSRFToken(key, a)
	if !VerifyCSRF(key, a, tok) || VerifyCSRF(key, b, tok) || VerifyCSRF(key, a, "") || VerifyCSRF(key, "", tok) {
		t.Fatal("csrf")
	}
}

func TestThrottle(t *testing.T) {
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{{0, 0}, {2, 0}, {3, 5 * time.Second}, {4, 10 * time.Second}, {10, 640 * time.Second}, {12, 15 * time.Minute}, {100, 15 * time.Minute}} {
		if got := Delay(tc.failures); got != tc.want {
			t.Errorf("Delay(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
	now := time.Now()
	later := now.Add(time.Minute)
	if th := Check(5, &later, nil, now); th.Wait != time.Minute || th.Locked {
		t.Errorf("got %+v", th)
	}
	if th := Check(10, nil, &now, now); !th.Locked {
		t.Error("want locked")
	}
	if !ShouldLock(true, 10) || ShouldLock(true, 9) || ShouldLock(false, 50) {
		t.Error("ShouldLock")
	}
}
