package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SessionTTL: phiên đăng nhập (JWT và cookie) hết hạn sau 24 giờ (spec §8).
const SessionTTL = 24 * time.Hour

const issuer = "streamfilm"

// MinSecretLen: HS256 cần khoá ít nhất 256 bit.
const MinSecretLen = 32

var ErrInvalidToken = errors.New("invalid session token")

// Identity là người dùng của request hiện tại, lấy từ JWT (không truy vấn DB).
type Identity struct {
	UserID string
	Role   Role
}

type claims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// Tokens phát và kiểm tra JWT HS256.
type Tokens struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokens(secret string, ttl time.Duration, now func() time.Time) (*Tokens, error) {
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("JWT_SECRET must be at least %d characters", MinSecretLen)
	}
	return &Tokens{secret: []byte(secret), ttl: ttl, now: now}, nil
}

// Issue trả token và thời điểm hết hạn.
func (t *Tokens) Issue(id Identity) (string, time.Time, error) {
	now := t.now()
	exp := now.Add(t.ttl)
	c := claims{Role: id.Role, RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   id.UserID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return s, exp, nil
}

// Parse chỉ nhận HS256 (chặn "alg":"none" và nhầm thuật toán), bắt buộc có exp, đúng issuer và iat (nếu có)
// không ở tương lai.
func (t *Tokens) Parse(s string) (Identity, error) {
	var c claims
	_, err := jwt.ParseWithClaims(s, &c, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if c.Subject == "" || !c.Role.Valid() {
		return Identity{}, fmt.Errorf("%w: missing subject or role", ErrInvalidToken)
	}
	return Identity{UserID: c.Subject, Role: c.Role}, nil
}
