package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeUsers là UserStore trong bộ nhớ.
type fakeUsers struct {
	byID map[string]User
}

func newFakeUsers() *fakeUsers { return &fakeUsers{byID: map[string]User{}} }

func (f *fakeUsers) Create(_ context.Context, u User) error {
	for _, x := range f.byID {
		if x.Email == u.Email {
			return ErrEmailTaken
		}
	}
	u.CreatedAt = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	f.byID[u.ID] = u
	return nil
}

func (f *fakeUsers) GetByEmail(_ context.Context, email string) (User, error) {
	for _, u := range f.byID {
		if u.Email == email {
			return u, nil
		}
	}
	return User{}, ErrUserNotFound
}

func (f *fakeUsers) GetByID(_ context.Context, id string) (User, error) {
	u, ok := f.byID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (f *fakeUsers) UpsertAdmin(_ context.Context, u User) (User, bool, error) {
	for id, x := range f.byID {
		if x.Email == u.Email {
			x.PasswordHash, x.Role = u.PasswordHash, RoleAdmin
			f.byID[id] = x
			return x, false, nil
		}
	}
	u.Role = RoleAdmin
	f.byID[u.ID] = u
	return u, true, nil
}

func newTestService(t *testing.T) (*Service, *fakeUsers) {
	t.Helper()
	now := time.Now()
	users := newFakeUsers()
	n := 0
	return &Service{Users: users, Tokens: newTestTokens(t, &now), NewID: func() string {
		n++
		return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
	}}, users
}

func TestRegisterLogin(t *testing.T) {
	s, users := newTestService(t)
	ctx := context.Background()

	u, err := s.Register(ctx, " An@Example.com ", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "an@example.com" || u.Role != RoleViewer || u.PasswordHash == "password123" {
		t.Errorf("user = %+v", u)
	}
	if _, err := s.Register(ctx, "an@example.com", "password456"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate: err = %v", err)
	}
	if _, err := s.Register(ctx, "bad", "password123"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad email: err = %v", err)
	}
	if _, err := s.Register(ctx, "b@example.com", "short"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("short password: err = %v", err)
	}
	if len(users.byID) != 1 {
		t.Errorf("users = %d, want 1", len(users.byID))
	}

	got, err := s.Login(ctx, "AN@example.com", "password123")
	if err != nil || got.ID != u.ID {
		t.Errorf("login = %+v, %v", got, err)
	}
	for _, c := range [][2]string{{"an@example.com", "wrong-pass"}, {"nobody@example.com", "password123"}, {"bad", "x"}} {
		if _, err := s.Login(ctx, c[0], c[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("login %v: err = %v", c, err)
		}
	}

	tok, _, err := s.Session(got)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Tokens.Parse(tok)
	if err != nil || id.UserID != u.ID || id.Role != RoleViewer {
		t.Errorf("session identity = %+v, %v", id, err)
	}
}

func TestSeedAdmin(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	a, created, err := s.SeedAdmin(ctx, "Admin@Streamfilm.local", "admin-pass-1")
	if err != nil || !created || a.Role != RoleAdmin || a.Email != "admin@streamfilm.local" {
		t.Fatalf("seed = %+v, %v, %v", a, created, err)
	}
	a2, created, err := s.SeedAdmin(ctx, "admin@streamfilm.local", "admin-pass-2")
	if err != nil || created || a2.ID != a.ID {
		t.Errorf("reseed = %+v, %v, %v", a2, created, err)
	}
	if _, err := s.Login(ctx, "admin@streamfilm.local", "admin-pass-2"); err != nil {
		t.Errorf("login with new password: %v", err)
	}
	if _, _, err := s.SeedAdmin(ctx, "admin@streamfilm.local", "short"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("short password: err = %v", err)
	}
}
