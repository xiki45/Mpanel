package mihomo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	base   *url.URL
	secret string
	http   *http.Client
}

func New(base *url.URL, secret string, timeout time.Duration) *Client {
	return &Client{base: base, secret: secret, http: &http.Client{Timeout: timeout}}
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return c.requestEscaped(ctx, method, path, "", body)
}

// requestEscaped is like request but also accepts a pre-escaped RawPath variant
// for callers that must embed arbitrary characters (e.g. slashes) in a single
// path segment without being re-encoded or interpreted as separators.
func (c *Client) requestEscaped(ctx context.Context, method, path, rawPath string, body io.Reader) (*http.Response, error) {
	pathOnly, query := path, ""
	if i := strings.IndexByte(path, '?'); i >= 0 {
		pathOnly, query = path[:i], path[i+1:]
	}
	u := c.base.ResolveReference(&url.URL{Path: pathOnly, RawQuery: query})
	if rawPath != "" {
		u.RawPath = rawPath
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mihomo unavailable: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("mihomo returned status %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) Reload(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodPut, "/configs?force=true", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
