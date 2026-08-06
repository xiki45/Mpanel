package mihomo

import (
	"context"
	"errors"
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

func TestOverviewAggregation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			w.Write([]byte(`{"version":"1.2.3"}`))
		case "/configs":
			w.Write([]byte(`{"mode":"rule"}`))
		case "/traffic":
			w.Write([]byte(`{"up":12,"down":34}`))
		case "/memory":
			w.Write([]byte(`{"inuse":56}`))
		case "/connections":
			w.Write([]byte(`{"uploadTotal":78,"downloadTotal":90,"connections":[{}]}`))
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	o := New(base, "", time.Second).Overview(context.Background())
	if !o.Online || o.Version != "1.2.3" || o.Connections != 1 || o.TotalDown != 90 {
		t.Fatalf("unexpected overview: %+v", o)
	}
}

func TestLogStreamCancellationClosesUpstream(t *testing.T) {
	disconnected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Write([]byte(`{"type":"info","payload":"ready"}` + "\n"))
		flusher.Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := New(base, "", time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.StreamLogs(ctx, func([]byte) error { cancel(); return context.Canceled })
	}()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected stream result: %v", err)
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not canceled")
	}
}
