package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mpanel/internal/auth"
	configmanager "mpanel/internal/config"
)

func testHandler() http.Handler {
	return New(auth.New("admin", "password", []byte("12345678901234567890123456789012")), &configmanager.Manager{}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
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
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/config", nil))
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

func TestCoreRoutesRequireAuth(t *testing.T) {
	h := testHandler()
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/config"},
		{"GET", "/api/config/backups"},
		{"GET", "/api/listeners"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "http://panel.test"+tc.path, nil))
		if w.Code != 401 {
			t.Fatalf("%s %s without auth got %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestRemovedRoutesReturn404JSON(t *testing.T) {
	h := testHandler()
	cookie := loginCookie(t, h)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/overview"},
		{"GET", "/api/proxies"},
		{"PATCH", "/api/mode"},
		{"GET", "/api/connections"},
		{"GET", "/api/logs/stream"},
		{"POST", "/api/service/start"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, "http://panel.test"+tc.path, strings.NewReader(`{}`))
		if tc.method != "GET" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.AddCookie(cookie)
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("%s %s got %d, want 404", tc.method, tc.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "接口不存在") {
			t.Fatalf("%s %s did not return a JSON 404: %s", tc.method, tc.path, w.Body.String())
		}
	}
}

func TestMutationOriginProtection(t *testing.T) {
	h := testHandler()
	cookie := loginCookie(t, h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://panel.test/api/auth/logout", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.test")
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin mutation got %d", w.Code)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://panel.test/api/auth/logout", strings.NewReader(`{}`))
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

func newSharesServer(t *testing.T, configYAML string) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(configYAML), 0600); err != nil {
		t.Fatal(err)
	}
	m := &configmanager.Manager{Path: path}
	return New(auth.New("admin", "password", []byte("12345678901234567890123456789012")), m, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}

const sharesVlessConfig = `mode: rule
listeners:
  - name: vless-r
    type: vless
    listen: 0.0.0.0
    port: 443
    network: tcp
    users:
      - username: alice
        uuid: 11111111-1111-1111-1111-111111111111
        flow: xtls-rprx-vision
      - username: bob
        uuid: 22222222-2222-2222-2222-222222222222
    reality-config:
      dest: example.com:443
      private-key: ejvNUybzw41Ku-82lY1vj5GDW4w_JkpwU7b839vny0g
      short-id:
        - 123456
      server-names:
        - example.com
`

func TestListenerShares(t *testing.T) {
	h := newSharesServer(t, sharesVlessConfig)
	cookie := loginCookie(t, h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://panel.test/api/listeners/vless-r/shares?host=example.com", nil)
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("shares got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"shares"`) {
		t.Fatalf("missing shares key: %s", body)
	}
	// Two users -> two shares.
	if strings.Count(body, `"label"`) != 2 {
		t.Fatalf("expected 2 shares, got: %s", body)
	}
	if !strings.Contains(body, "vless://") || !strings.Contains(body, "pbk=") || !strings.Contains(body, "security=reality") {
		t.Fatalf("missing vless reality uri: %s", body)
	}
	if strings.Contains(body, "ejvNUybzw41Ku-82lY1vj5GDW4w_JkpwU7b839vny0g") {
		t.Fatal("private key leaked into shares response")
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("missing qr data url: %s", body)
	}
}

func TestListenerSharesHostValidation(t *testing.T) {
	h := newSharesServer(t, sharesVlessConfig)
	cookie := loginCookie(t, h)
	for _, query := range []string{
		"host=", "host=http://evil.com", "host=example.com:443",
		"host=example.com/path", "host=evil.com?x=1", "host=user@example.com",
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://panel.test/api/listeners/vless-r/shares?"+query, nil)
		r.AddCookie(cookie)
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("host query %q got %d, want 400: %s", query, w.Code, w.Body.String())
		}
	}
}

func TestListenerSharesNotFound(t *testing.T) {
	h := newSharesServer(t, sharesVlessConfig)
	cookie := loginCookie(t, h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://panel.test/api/listeners/nope/shares?host=example.com", nil)
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("not found got %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestListenerSharesRequiresAuth(t *testing.T) {
	h := newSharesServer(t, sharesVlessConfig)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://panel.test/api/listeners/vless-r/shares?host=example.com", nil)
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("shares without auth got %d, want 401", w.Code)
	}
}

func TestListenerSharesMixedProtocolEmpty(t *testing.T) {
	h := newSharesServer(t, "mode: rule\nlisteners:\n  - name: socks1\n    type: socks\n    listen: 0.0.0.0\n    port: 1080\n")
	cookie := loginCookie(t, h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://panel.test/api/listeners/socks1/shares?host=example.com", nil)
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("socks shares got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"shares":[]`) {
		t.Fatalf("socks should return empty shares: %s", w.Body.String())
	}
}

func TestListenerSharesSupportedProtocolMissingCredentialReturns400(t *testing.T) {
	// A supported protocol with no credentials must return a clear business
	// error (not a silently empty shares list).
	h := newSharesServer(t, "mode: rule\nlisteners:\n  - name: ss1\n    type: shadowsocks\n    listen: 0.0.0.0\n    port: 8388\n")
	cookie := loginCookie(t, h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://panel.test/api/listeners/ss1/shares?host=example.com", nil)
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("shadowsocks without credentials got %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "error") {
		t.Fatalf("expected an error payload: %s", w.Body.String())
	}
}

func TestListenerSharesRealityInvalidKey400NoLeak(t *testing.T) {
	// A vless Reality with an invalid private key returns 400 and never leaks
	// the key value in the response.
	const badKey = "totally-invalid-key!!!"
	cfg := "mode: rule\nlisteners:\n  - name: vless-bad\n    type: vless\n    listen: 0.0.0.0\n    port: 443\n    users:\n      - username: u\n        uuid: 11111111-1111-1111-1111-111111111111\n    reality-config:\n      private-key: " + badKey + "\n      short-id:\n        - 123456\n      server-names:\n        - example.com\n"
	h := newSharesServer(t, cfg)
	cookie := loginCookie(t, h)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://panel.test/api/listeners/vless-bad/shares?host=example.com", nil)
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("invalid reality key got %d, want 400: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), badKey) {
		t.Fatalf("invalid reality key leaked into response: %s", w.Body.String())
	}
}

func TestSecurityHeadersAllowDataImages(t *testing.T) {
	h := testHandler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data:") {
		t.Fatalf("CSP does not allow data: images: %q", csp)
	}
}
