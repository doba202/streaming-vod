package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo lưu tài khoản trong bảng users.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const userColumns = `id::text, email, password_hash, role, created_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	var role string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &role, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	u.Role = Role(role)
	return u, err
}

// Create thêm tài khoản; email đã tồn tại thì trả ErrEmailTaken.
func (r *Repo) Create(ctx context.Context, u User) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, $2, $3, $4)`,
		u.ID, u.Email, u.PasswordHash, string(u.Role))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return ErrEmailTaken
	}
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (r *Repo) GetByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

func (r *Repo) GetByID(ctx context.Context, id string) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// UpsertAdmin tạo admin, hoặc nếu email đã có thì đặt lại mật khẩu và nâng role lên admin.
// created = true khi là bản ghi mới (xmax = 0 nghĩa là dòng vừa INSERT, không phải UPDATE).
func (r *Repo) UpsertAdmin(ctx context.Context, u User) (User, bool, error) {
	var created bool
	out, err := scanUserCreated(r.pool.QueryRow(ctx, `INSERT INTO users (id, email, password_hash, role)
		VALUES ($1, $2, $3, 'admin')
		ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash, role = 'admin'
		RETURNING `+userColumns+`, (xmax = 0)`, u.ID, u.Email, u.PasswordHash), &created)
	if err != nil {
		return User{}, false, fmt.Errorf("upsert admin: %w", err)
	}
	return out, created, nil
}

func scanUserCreated(row pgx.Row, created *bool) (User, error) {
	var u User
	var role string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &role, &u.CreatedAt, created)
	u.Role = Role(role)
	return u, err
}
