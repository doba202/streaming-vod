// Package auth: tài khoản (bcrypt), phiên đăng nhập bằng JWT HS256 trong cookie HttpOnly,
// middleware phân quyền admin/viewer.
package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

func (r Role) Valid() bool { return r == RoleAdmin || r == RoleViewer }

type User struct {
	ID, Email    string
	PasswordHash string
	Role         Role
	CreatedAt    time.Time
}

var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUserNotFound       = errors.New("user not found")
)

const (
	MinPasswordLen = 8
	MaxPasswordLen = 72 // bcrypt chỉ dùng 72 byte đầu; dài hơn thì từ chối thay vì cắt ngầm
)

// hashCost là cost của bcrypt. Biến (không phải hằng) để test hạ xuống bcrypt.MinCost.
var hashCost = 12

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), hashCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(b), nil
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NormalizeEmail cắt khoảng trắng, chuyển chữ thường và kiểm tra dạng "a@b" (không nhận "Tên <a@b>").
func NormalizeEmail(s string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(s))
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || len(e) > 254 {
		return "", fmt.Errorf("%w: email is not valid", ErrInvalidInput)
	}
	return e, nil
}

func ValidatePassword(pw string) error {
	if n := len(pw); n < MinPasswordLen || n > MaxPasswordLen {
		return fmt.Errorf("%w: password must be %d-%d bytes", ErrInvalidInput, MinPasswordLen, MaxPasswordLen)
	}
	return nil
}
