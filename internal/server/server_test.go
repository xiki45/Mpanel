package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"mpanel/internal/auth"
	configmanager "mpanel/internal/config"
	"mpanel/internal/mihomo"
)

type fakeMihomo struct {
	setModeErr   error
	selectErr    error
	proxies      map[string]mihomo.Proxy
	proxiesErr   error
	modeCalls    []string
	selectCalls  []string
	selectGroup  string
}

func (fakeMihomo) Overview(context.Context) mihomo.Overview                      { return mihomo.Overview{Online: true} }
func (fakeMihomo) Reload(context.Context) error                                  { return nil }
func (fakeMihomo) StreamLogs(ctx context.Context, emit func([]byte) error) error { return nil }
func (f *fakeMihomo) Proxies(context.Context) (map[string]mihomo.Proxy, error) {
	if f.proxiesErr != nil {
		return nil, f.proxiesErr
	}
	if f.proxies == nil {
		return map[string]mihomo.Proxy{}, nil
	}
	return f.proxies, nil
}
func (f *fakeMihomo) SetMode(_ context.Context, mode string) error {
	f.modeCalls = append(f.modeCalls, mode)
	return f.setModeErr
}
func (f *fakeMihomo) SelectProxy(_ context.Context, group, name string) error {
	f.selectGroup = group
	f.selectCalls = append(f.selectCalls, name)
	return f.selectErr
}

type fakeService struct{}

func (fakeService) Action(context.Context, string) error { return nil }

func testHandler() http.Handler {
	return testHandlerWith(&fakeMihomo{})
}

func testHandlerWith(m Mihomo) http.Handler {
	return New(auth.New("admin", "password", []byte("12345678901234567890123456789012")), &configmanager.Manager{}, m, fakeService{}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
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

func authedRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "http://panel.test"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(loginCookie(t, h))
	h.ServeHTTP(w, r)
	return w
}

func TestMihomoRoutesRequireAuth(t *testing.T) {
	h := testHandler()
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/proxies"},
		{"PATCH", "/api/mode"},
		{"PUT", "/api/proxies/GLOBAL"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, "http://panel.test"+tc.path, nil)
		if tc.method != "GET" {
			r.Header.Set("Content-Type", "application/json")
		}
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("%s %s without auth got %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestGetProxies(t *testing.T) {
	fake := &fakeMihomo{proxies: map[string]mihomo.Proxy{
		"GLOBAL": {Type: "Selector", Now: "node-a", All: []string{"node-a", "node-b"}},
	}}
	h := testHandlerWith(fake)
	w := authedRequest(t, h, "GET", "/api/proxies", "")
	if w.Code != 200 {
		t.Fatalf("get proxies got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"GLOBAL"`) || !strings.Contains(w.Body.String(), `"node-a"`) {
		t.Fatalf("proxies missing in response: %s", w.Body.String())
	}
}

func TestPatchModeWhitelist(t *testing.T) {
	fake := &fakeMihomo{}
	h := testHandlerWith(fake)
	for _, mode := range []string{"direct", "rule", "global"} {
		fake.modeCalls = nil
		w := authedRequest(t, h, "PATCH", "/api/mode", `{"mode":"`+mode+`"}`)
		if w.Code != 200 {
			t.Fatalf("set mode %q got %d: %s", mode, w.Code, w.Body.String())
		}
		if len(fake.modeCalls) != 1 || fake.modeCalls[0] != mode {
			t.Fatalf("mode %q not forwarded: %v", mode, fake.modeCalls)
		}
	}
	for _, mode := range []string{"", "rules", "direct;rm -rf", "Rule"} {
		w := authedRequest(t, h, "PATCH", "/api/mode", `{"mode":"`+mode+`"}`)
		if w.Code != 400 {
			t.Fatalf("invalid mode %q got %d: %s", mode, w.Code, w.Body.String())
		}
	}
}

func TestSelectProxyWithSpecialChars(t *testing.T) {
	fake := &fakeMihomo{}
	h := testHandlerWith(fake)
	// Group and node name with characters that must survive URL handling.
	group := "My Group/50%"
	escapedGroup := url.PathEscape(group)
	w := authedRequest(t, h, "PUT", "/api/proxies/"+escapedGroup, `{"name":"节点 1"}`)
	if w.Code != 200 {
		t.Fatalf("select proxy got %d: %s", w.Code, w.Body.String())
	}
	if fake.selectGroup != group {
		t.Fatalf("group not decoded correctly: %q", fake.selectGroup)
	}
	if len(fake.selectCalls) != 1 || fake.selectCalls[0] != "节点 1" {
		t.Fatalf("node name not forwarded: %v", fake.selectCalls)
	}
}

func TestSelectProxyInvalidPathParam(t *testing.T) {
	fake := &fakeMihomo{}
	h := testHandlerWith(fake)
	// Over-long group (strict > maxPathParam).
	long := strings.Repeat("a", maxPathParam+1)
	w := authedRequest(t, h, "PUT", "/api/proxies/"+long, `{"name":"node"}`)
	if w.Code != 400 {
		t.Fatalf("over-long group got %d, want 400", w.Code)
	}
	// Missing/empty node name.
	w = authedRequest(t, h, "PUT", "/api/proxies/GLOBAL", `{"name":""}`)
	if w.Code != 400 {
		t.Fatalf("empty node name got %d, want 400", w.Code)
	}
	// No group segment at all -> route does not match, falls through to 404.
	w = authedRequest(t, h, "PUT", "/api/proxies/", `{"name":"node"}`)
	if w.Code != 404 {
		t.Fatalf("missing group got %d, want 404", w.Code)
	}
}
