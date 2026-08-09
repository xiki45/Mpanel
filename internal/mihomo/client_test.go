package mihomo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestBearerHeaderAndErrors(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		http.Error(w, "secret details", 502)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "top-secret", time.Second)
	if err := c.Reload(context.Background()); err == nil {
		t.Fatal("non-2xx response accepted")
	}
	if auth != "Bearer top-secret" {
		t.Fatalf("unexpected authorization header %q", auth)
	}
}

func TestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond) }))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", 10*time.Millisecond)
	if c.Reload(context.Background()) == nil {
		t.Fatal("timeout was not reported")
	}
}
