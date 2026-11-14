package auth

import (
	"context"
	"errors"
	"testing"

	"streamfilm/internal/db"
	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

func TestRepo(t *testing.T) {
	dsn := testutil.StartPostgres(t)
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := NewRepo(pool)

	const (
		idV = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		idA = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	if err := r.Create(ctx, User{ID: idV, Email: "v@example.com", PasswordHash: "h1", Role: RoleViewer}); err != nil {
		t.Fatal(err)
	}
	err = r.Create(ctx, User{ID: idA, Email: "v@example.com", PasswordHash: "h2", Role: RoleViewer})
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email: err = %v", err)
	}

	u, err := r.GetByEmail(ctx, "v@example.com")
	if err != nil || u.ID != idV || u.Role != RoleViewer || u.PasswordHash != "h1" || u.CreatedAt.IsZero() {
		t.Errorf("GetByEmail = %+v, %v", u, err)
	}
	if u, err := r.GetByID(ctx, idV); err != nil || u.Email != "v@example.com" {
		t.Errorf("GetByID = %+v, %v", u, err)
	}
	if _, err := r.GetByEmail(ctx, "none@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("missing email: err = %v", err)
	}
	if _, err := r.GetByID(ctx, idA); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("missing id: err = %v", err)
	}

	admin, created, err := r.UpsertAdmin(ctx, User{ID: idA, Email: "admin@example.com", PasswordHash: "ha"})
	if err != nil || !created || admin.ID != idA || admin.Role != RoleAdmin {
		t.Errorf("UpsertAdmin new = %+v, %v, %v", admin, created, err)
	}
	// Email đã có (viewer): giữ id cũ, đổi mật khẩu, nâng role.
	up, created, err := r.UpsertAdmin(ctx, User{ID: idA, Email: "v@example.com", PasswordHash: "h9"})
	if err != nil || created || up.ID != idV || up.Role != RoleAdmin || up.PasswordHash != "h9" {
		t.Errorf("UpsertAdmin existing = %+v, %v, %v", up, created, err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role)
		VALUES ('cccccccc-cccc-cccc-cccc-cccccccccccc', 'Upper@example.com', 'h', 'viewer')`); err == nil {
		t.Error("CHECK email = lower(email) phải chặn email chữ hoa")
	}
}
