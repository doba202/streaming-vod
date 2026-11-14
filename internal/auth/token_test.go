package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-0123456789-0123456789-abcdef"

func newTestTokens(t *testing.T, now *time.Time) *Tokens {
	t.Helper()
	tk, err := NewTokens(testSecret, SessionTTL, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestNewTokensRejectsShortSecret(t *testing.T) {
	if _, err := NewTokens("short", SessionTTL, time.Now); err == nil {
		t.Fatal("want error for short secret")
	}
}

func TestIssueParse(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	tk := newTestTokens(t, &now)
	tok, exp, err := tk.Issue(Identity{UserID: "u1", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(24 * time.Hour)) {
		t.Errorf("exp = %v", exp)
	}
	id, err := tk.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if id != (Identity{UserID: "u1", Role: RoleAdmin}) {
		t.Errorf("identity = %+v", id)
	}

	now = now.Add(24*time.Hour + time.Second)
	if _, err := tk.Parse(tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token: err = %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	tk := newTestTokens(t, &now)
	good, _, _ := tk.Issue(Identity{UserID: "u1", Role: RoleViewer})

	other, err := NewTokens(strings.Repeat("x", 40), SessionTTL, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	otherTok, _, _ := other.Issue(Identity{UserID: "u1", Role: RoleAdmin})

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "u1", "role": "admin", "iss": "streamfilm", "exp": now.Add(time.Hour).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	noExp, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "role": "admin", "iss": "streamfilm",
	}).SignedString([]byte(testSecret))
	badRole, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "role": "root", "iss": "streamfilm", "exp": now.Add(time.Hour).Unix(),
	}).SignedString([]byte(testSecret))
	parts := strings.Split(good, ".")
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]

	cases := map[string]string{
		"other secret": otherTok, "alg none": none, "no exp": noExp, "bad role": badRole,
		"tampered": tampered, "garbage": "abc", "empty": "",
	}
	for name, tok := range cases {
		if _, err := tk.Parse(tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
