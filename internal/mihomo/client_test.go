package mihomo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestProxiesParsesAndSendsBearer(t *testing.T) {
	var auth, method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		method = r.Method
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"proxies":{"DIRECT":{"type":"Direct"},"GLOBAL":{"type":"Selector","now":"node-a","all":["node-a","node-b"]}}}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "s3cret", time.Second)
	proxies, err := c.Proxies(context.Background())
	if err != nil {
		t.Fatalf("Proxies failed: %v", err)
	}
	if method != http.MethodGet || path != "/proxies" {
		t.Fatalf("unexpected request: method=%s path=%s", method, path)
	}
	if auth != "Bearer s3cret" {
		t.Fatalf("unexpected authorization header %q", auth)
	}
	g, ok := proxies["GLOBAL"]
	if !ok || g.Type != "Selector" || g.Now != "node-a" || len(g.All) != 2 {
		t.Fatalf("unexpected proxies: %+v", proxies)
	}
}

func TestProxiesNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", time.Second)
	if _, err := c.Proxies(context.Background()); err == nil {
		t.Fatal("non-2xx proxies response accepted")
	}
}

func TestSetModePATCHBodyAnd204(t *testing.T) {
	var method, path, contentType, auth string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		contentType = r.Header.Get("Content-Type")
		auth = r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "tok", time.Second)
	if err := c.SetMode(context.Background(), "global"); err != nil {
		t.Fatalf("SetMode failed: %v", err)
	}
	if method != http.MethodPatch || path != "/configs" {
		t.Fatalf("unexpected request: method=%s path=%s", method, path)
	}
	if contentType != "application/json" || auth != "Bearer tok" {
		t.Fatalf("unexpected headers: ct=%q auth=%q", contentType, auth)
	}
	if strings.TrimSpace(string(body)) != `{"mode":"global"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestSetModeNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", 403)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", time.Second)
	if err := c.SetMode(context.Background(), "rule"); err == nil {
		t.Fatal("non-2xx mode response accepted")
	}
}

func TestSelectProxyEscapesGroupAndSends204(t *testing.T) {
	var method, escapedPath, contentType string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		escapedPath = r.URL.EscapedPath()
		contentType = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "tok", time.Second)
	group := "My Group/50%off"
	if err := c.SelectProxy(context.Background(), group, "node-1"); err != nil {
		t.Fatalf("SelectProxy failed: %v", err)
	}
	if method != http.MethodPut {
		t.Fatalf("unexpected method: %s", method)
	}
	// Group must be a single escaped path segment (slash encoded as %2F).
	if escapedPath != "/proxies/My%20Group%2F50%25off" {
		t.Fatalf("unexpected escaped path: %q", escapedPath)
	}
	if contentType != "application/json" {
		t.Fatalf("unexpected content type: %q", contentType)
	}
	if strings.TrimSpace(string(body)) != `{"name":"node-1"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestSelectProxyNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", 404)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", time.Second)
	if err := c.SelectProxy(context.Background(), "GLOBAL", "node"); err == nil {
		t.Fatal("non-2xx select response accepted")
	}
}

func TestConnectionsNormalizesFields(t *testing.T) {
	var auth, method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		method = r.Method
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"downloadTotal": 4096,
			"uploadTotal": 1024,
			"connections": [{
				"id": "conn-1",
				"upload": 10,
				"download": 20,
				"start": "2025-01-01T00:00:00Z",
				"chains": ["节点选择", "HK-01"],
				"rule": "DomainSuffix",
				"rulePayload": "example.com",
				"metadata": {
					"network": "tcp",
					"type": "HTTP",
					"sourceIP": "192.168.1.2",
					"destinationIP": "10.0.0.1",
					"destinationPort": "443",
					"host": "example.com",
					"sniffHost": ""
				}
			}]
		}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "s3cret", time.Second)
	conns, err := c.Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections failed: %v", err)
	}
	if method != http.MethodGet || path != "/connections" {
		t.Fatalf("unexpected request: method=%s path=%s", method, path)
	}
	if auth != "Bearer s3cret" {
		t.Fatalf("unexpected authorization header %q", auth)
	}
	if conns.DownloadTotal != 4096 || conns.UploadTotal != 1024 {
		t.Fatalf("unexpected totals: %+v", conns)
	}
	if len(conns.Connections) != 1 {
		t.Fatalf("unexpected connection count: %d", len(conns.Connections))
	}
	got := conns.Connections[0]
	if got.ID != "conn-1" || got.Host != "example.com:443" || got.Type != "HTTP/TCP" {
		t.Fatalf("unexpected normalized connection: %+v", got)
	}
	if got.Network != "tcp" || got.SourceIP != "192.168.1.2" {
		t.Fatalf("unexpected network/source: %+v", got)
	}
	if got.Rule != "DomainSuffix(example.com)" {
		t.Fatalf("unexpected rule: %q", got.Rule)
	}
	if len(got.Chains) != 2 || got.Chains[0] != "节点选择" || got.Chains[1] != "HK-01" {
		t.Fatalf("chains order not preserved: %v", got.Chains)
	}
	if conns.Truncated {
		t.Fatal("expected truncated to be false")
	}
}

func TestConnectionsHostFallbackToDestinationIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"downloadTotal":0,"uploadTotal":0,"connections":[{
			"id":"c",
			"metadata":{"network":"udp","type":"","destinationIP":"10.0.0.9","destinationPort":"53"}
		}]}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", time.Second)
	conns, err := c.Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections failed: %v", err)
	}
	got := conns.Connections[0]
	if got.Host != "10.0.0.9:53" {
		t.Fatalf("unexpected host fallback: %q", got.Host)
	}
	if got.Type != "UDP" {
		t.Fatalf("unexpected type with empty network type: %q", got.Type)
	}
}

func TestConnectionsChainsNullBecomesEmptySlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"downloadTotal":0,"uploadTotal":0,"connections":[{"id":"c","chains":null}]}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", time.Second)
	conns, err := c.Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections failed: %v", err)
	}
	got := conns.Connections[0]
	if got.Chains == nil {
		t.Fatal("chains should not be nil")
	}
	if len(got.Chains) != 0 {
		t.Fatalf("expected empty chains, got %v", got.Chains)
	}
}

func TestConnectionsTruncatesAtLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"downloadTotal":0,"uploadTotal":0,"connections":[`)
	for i := 0; i < maxConnections+5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"c"}`)
	}
	b.WriteString(`]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(b.String()))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", time.Second)
	conns, err := c.Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections failed: %v", err)
	}
	if !conns.Truncated {
		t.Fatal("expected truncated to be true")
	}
	if len(conns.Connections) != maxConnections {
		t.Fatalf("expected %d connections, got %d", maxConnections, len(conns.Connections))
	}
}

func TestConnectionsNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := New(base, "", time.Second)
	if _, err := c.Connections(context.Background()); err == nil {
		t.Fatal("non-2xx connections response accepted")
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
