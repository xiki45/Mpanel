package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mpanel/internal/auth"
	configmanager "mpanel/internal/config"
	"mpanel/internal/mihomo"
)

type fakeMihomo struct{}

func (fakeMihomo) Overview(context.Context) mihomo.Overview                      { return mihomo.Overview{Online: true} }
func (fakeMihomo) Reload(context.Context) error                                  { return nil }
func (fakeMihomo) StreamLogs(ctx context.Context, emit func([]byte) error) error { return nil }

type fakeService struct{}

func (fakeService) Action(context.Context, string) error { return nil }

func testHandler() http.Handler {
	return New(auth.New("admin", "password", []byte("12345678901234567890123456789012")), &configmanager.Manager{}, fakeMihomo{}, fakeService{}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}
func loginCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://panel.test/api/auth/login", strings.NewReader(`{"username":"admin","password":"password"}`))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login failed: %s", w.Body.String())
	}
	return w.Result().Cookies()[0]
}

func TestProtectedRouteAndLoginFailure(t *testing.T) {
	h := testHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/overview", nil))
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "用户名或密码错误") {
		t.Fatalf("unexpected failure: %d %s", w.Code, w.Body.String())
	}
}

func TestMutationOriginProtection(t *testing.T) {
	h := testHandler()
	cookie := loginCookie(t, h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://panel.test/api/service/start", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.test")
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin mutation got %d", w.Code)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://panel.test/api/service/start", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("origin-less CLI request got %d: %s", w.Code, w.Body.String())
	}
}

func TestLoginOriginProtection(t *testing.T) {
	h := testHandler()
	body := `{"username":"admin","password":"password"}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://panel.test/api/auth/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.test")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login got %d: %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("cross-origin login set a session cookie")
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://panel.test/api/auth/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "Application/JSON; Charset=UTF-8")
	r.Header.Set("Origin", "http://panel.test")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin login got %d: %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 1 {
		t.Fatal("same-origin login did not set a session cookie")
	}
}
