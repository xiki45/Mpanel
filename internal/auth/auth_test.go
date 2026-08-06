package auth

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestCredentialsAndSession(t *testing.T) {
	m := New("admin", "correct", []byte("12345678901234567890123456789012"))
	if !m.ValidCredentials("admin", "correct") || m.ValidCredentials("admin", "wrong") {
		t.Fatal("credential validation mismatch")
	}
	w := httptest.NewRecorder()
	m.SetCookie(w, false)
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	if err := m.Authenticate(r); err != nil {
		t.Fatalf("valid cookie rejected: %v", err)
	}
	cookie.Value += "x"
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	if m.Authenticate(r) == nil {
		t.Fatal("tampered cookie accepted")
	}
}

func TestExpiredSession(t *testing.T) {
	m := New("admin", "correct", []byte("12345678901234567890123456789012"))
	now := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time { return now }
	w := httptest.NewRecorder()
	m.SetCookie(w, false)
	cookie := w.Result().Cookies()[0]
	m.now = func() time.Time { return now.Add(25 * time.Hour) }
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	if m.Authenticate(r) == nil {
		t.Fatal("expired cookie accepted")
	}
}
