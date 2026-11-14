package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddleware(t *testing.T) {
	now := time.Now()
	tk := newTestTokens(t, &now)
	admin, _, _ := tk.Issue(Identity{UserID: "a1", Role: RoleAdmin})
	viewer, _, _ := tk.Issue(Identity{UserID: "v1", Role: RoleViewer})

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		w.Write([]byte(id.UserID))
	})
	cases := []struct {
		name, cookie string
		mw           func(http.Handler) http.Handler
		want         int
	}{
		{"user: no cookie", "", RequireUser, 401},
		{"user: bad token", "garbage", RequireUser, 401},
		{"user: viewer", viewer, RequireUser, 200},
		{"admin: no cookie", "", RequireAdmin, 401},
		{"admin: viewer", viewer, RequireAdmin, 403},
		{"admin: admin", admin, RequireAdmin, 200},
	}
	for _, c := range cases {
		h := tk.Authenticate(c.mw(ok))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: c.cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: code = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestSessionCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "tok", time.Now().Add(SessionTTL), false)
	c := rec.Result().Cookies()[0]
	if c.Name != CookieName || c.Value != "tok" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
		c.Path != "/" || c.Secure || c.MaxAge < 86390 || c.MaxAge > 86400 {
		t.Errorf("cookie = %+v", c)
	}
	rec = httptest.NewRecorder()
	ClearSessionCookie(rec, true)
	c = rec.Result().Cookies()[0]
	if c.Name != CookieName || c.Value != "" || c.MaxAge != -1 || !c.Secure {
		t.Errorf("clear cookie = %+v", c)
	}
}
