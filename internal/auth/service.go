package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// UserStore là phần của Repo mà Service cần.
type UserStore interface {
	Create(ctx context.Context, u User) error
	GetByEmail(ctx context.Context, email string) (User, error)
	GetByID(ctx context.Context, id string) (User, error)
	UpsertAdmin(ctx context.Context, u User) (User, bool, error)
}

type Service struct {
	Users  UserStore
	Tokens *Tokens
	NewID  func() string
}

// dummyHash dùng khi email không tồn tại, để Login tốn thời gian như khi sai mật khẩu
// (không lộ email nào đã đăng ký qua thời gian phản hồi).
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("streamfilm-dummy-password")
	return h
})

// WarmUp tính sẵn dummyHash (bcrypt, ~250ms) để request đăng nhập đầu tiên với email lạ không chậm hơn
// các request sau (chênh lệch thời gian đó lộ email chưa đăng ký). api gọi lúc khởi động.
func WarmUp() { dummyHash() }

// Register tạo tài khoản viewer.
func (s *Service) Register(ctx context.Context, email, password string) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	u := User{ID: s.NewID(), Email: email, PasswordHash: hash, Role: RoleViewer}
	if err := s.Users.Create(ctx, u); err != nil {
		return User{}, err
	}
	return s.Users.GetByID(ctx, u.ID)
}

// Login kiểm tra email + mật khẩu. Sai email hay sai mật khẩu đều trả ErrInvalidCredentials.
func (s *Service) Login(ctx context.Context, email, password string) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		CheckPassword(dummyHash(), password)
		return User{}, ErrInvalidCredentials
	}
	u, err := s.Users.GetByEmail(ctx, email)
	if errors.Is(err, ErrUserNotFound) {
		CheckPassword(dummyHash(), password)
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if !CheckPassword(u.PasswordHash, password) {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

// Session phát JWT cho user.
func (s *Service) Session(u User) (string, time.Time, error) {
	return s.Tokens.Issue(Identity{UserID: u.ID, Role: u.Role})
}

// SeedAdmin tạo admin hoặc đặt lại mật khẩu + nâng quyền nếu email đã có (make seed-admin).
func (s *Service) SeedAdmin(ctx context.Context, email, password string) (User, bool, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, false, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, false, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, false, err
	}
	return s.Users.UpsertAdmin(ctx, User{ID: s.NewID(), Email: email, PasswordHash: hash})
}
