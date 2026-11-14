package auth

import (
	"context"
	"net/http"
	"time"

	"streamfilm/internal/httpx"
)

// CookieName là cookie chứa JWT phiên đăng nhập.
const CookieName = "sf_session"

type ctxKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Authenticate đọc cookie phiên; token hợp lệ thì gắn Identity vào context.
// Không chặn request: RequireUser / RequireAdmin quyết định.
func (t *Tokens) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(CookieName); err == nil {
			if id, err := t.Parse(c.Value); err == nil {
				r = r.WithContext(WithIdentity(r.Context(), id))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireUser trả 401 nếu chưa đăng nhập.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "login required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin trả 401 nếu chưa đăng nhập, 403 nếu không phải admin.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "login required")
			return
		}
		if id.Role != RoleAdmin {
			httpx.WriteError(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetSessionCookie đặt cookie HttpOnly, SameSite=Lax, sống tới exp (spec §8).
func SetSessionCookie(w http.ResponseWriter, token string, exp time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Expires: exp, MaxAge: int(time.Until(exp).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
}

// ClearSessionCookie xoá cookie phiên phía trình duyệt.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
}
