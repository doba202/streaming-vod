package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newTestRouter(t *testing.T) (http.Handler, *Service) {
	t.Helper()
	s, _ := newTestService(t)
	r := chi.NewRouter()
	r.Use(s.Tokens.Authenticate)
	(&Handler{Svc: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r, s
}

func do(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", CookieName)
	return nil
}

func TestHandlerFlow(t *testing.T) {
	h, _ := newTestRouter(t)

	rec := do(h, "POST", "/api/auth/register", `{"email":"V@Example.com","password":"password123"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var u UserJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.Email != "v@example.com" || u.Role != RoleViewer || u.ID == "" {
		t.Errorf("register body = %+v", u)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Error("response không được chứa password/hash")
	}
	regCookie := sessionCookie(t, rec)

	if rec := do(h, "POST", "/api/auth/register", `{"email":"v@example.com","password":"password123"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate register: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/auth/register", `{"email":"x","password":"password123"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad email: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/auth/register", `{"email":"y@example.com","password":"password123","role":"admin"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field role phải bị từ chối: %d", rec.Code)
	}

	if rec := do(h, "POST", "/api/auth/login", `{"email":"v@example.com","password":"wrong-password"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", rec.Code)
	}
	rec = do(h, "POST", "/api/auth/login", `{"email":"v@example.com","password":"password123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	loginCookie := sessionCookie(t, rec)

	if rec := do(h, "GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("me without cookie: %d", rec.Code)
	}
	for _, c := range []*http.Cookie{regCookie, loginCookie} {
		rec := do(h, "GET", "/api/me", "", &http.Cookie{Name: CookieName, Value: c.Value})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"v@example.com"`) {
			t.Errorf("me: %d %s", rec.Code, rec.Body)
		}
	}

	rec = do(h, "POST", "/api/auth/logout", "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	if c := sessionCookie(t, rec); c.MaxAge != -1 || c.Value != "" {
		t.Errorf("logout cookie = %+v", c)
	}
}

func TestMeDeletedUser(t *testing.T) {
	h, s := newTestRouter(t)
	tok, _, err := s.Tokens.Issue(Identity{UserID: "00000000-0000-0000-0000-000000000999", Role: RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(h, "GET", "/api/me", "", &http.Cookie{Name: CookieName, Value: tok})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("me for deleted user: %d", rec.Code)
	}
}
