package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const CookieName = "mpanel_session"

type Manager struct {
	username string
	password string
	secret   []byte
	now      func() time.Time
}

type claims struct {
	Username string `json:"u"`
	Expires  int64  `json:"e"`
}

func New(username, password string, secret []byte) *Manager {
	return &Manager{username: username, password: password, secret: append([]byte(nil), secret...), now: time.Now}
}

func (m *Manager) ValidCredentials(username, password string) bool {
	return subtle.ConstantTimeCompare([]byte(username), []byte(m.username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(password), []byte(m.password)) == 1
}

func (m *Manager) SetCookie(w http.ResponseWriter, secure bool) {
	payload, _ := json.Marshal(claims{Username: m.username, Expires: m.now().Add(24 * time.Hour).Unix()})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(encoded))
	token := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", MaxAge: 86400, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}

func (m *Manager) ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}

func (m *Manager) Authenticate(r *http.Request) error {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return errors.New("unauthenticated")
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return errors.New("unauthenticated")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("unauthenticated")
	}
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return errors.New("unauthenticated")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("unauthenticated")
	}
	var c claims
	if json.Unmarshal(payload, &c) != nil || c.Username != m.username || c.Expires <= m.now().Unix() {
		return errors.New("unauthenticated")
	}
	return nil
}

func IsSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
