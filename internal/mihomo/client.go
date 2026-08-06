package mihomo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type Overview struct {
	Online      bool   `json:"online"`
	Version     string `json:"version,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
	TotalUpload int64  `json:"totalUpload"`
	TotalDown   int64  `json:"totalDownload"`
	Memory      int64  `json:"memory"`
	Connections int    `json:"connections"`
	Message     string `json:"message,omitempty"`
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

func (c *Client) decode(ctx context.Context, path string, dst any) error {
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(dst); err != nil {
		return errors.New("invalid response from mihomo")
	}
	return nil
}

// decodeStream reads a single JSON object from a streaming endpoint (e.g.
// /traffic, /memory) that continuously pushes data without closing the
// connection. These endpoints may stay idle (no data pushed when there is
// no traffic), so a short read deadline is used to avoid blocking until the
// caller's context expires.
func (c *Client) decodeStream(ctx context.Context, path string, dst any) error {
	streamCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	resp, err := c.request(streamCtx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return errors.New("invalid response from mihomo")
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[:i]
	}
	if len(data) == 0 {
		return errors.New("no data from stream")
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return errors.New("invalid response from mihomo")
	}
	return nil
}

func (c *Client) Overview(ctx context.Context) Overview {
	o := Overview{Online: true}
	var version struct {
		Version string `json:"version"`
	}
	if err := c.decode(ctx, "/version", &version); err != nil {
		return Overview{Online: false, Message: "无法连接 mihomo"}
	}
	o.Version = version.Version
	var cfg struct {
		Mode string `json:"mode"`
	}
	if err := c.decode(ctx, "/configs", &cfg); err != nil {
		return Overview{Online: false, Message: "状态数据不可用"}
	}
	o.Mode = cfg.Mode
	var traffic struct{ Up, Down int64 }
	if err := c.decodeStream(ctx, "/traffic", &traffic); err == nil {
		o.Upload, o.Download = traffic.Up, traffic.Down
	}
	var memory struct {
		Inuse int64 `json:"inuse"`
	}
	if err := c.decodeStream(ctx, "/memory", &memory); err == nil {
		o.Memory = memory.Inuse
	}
	var connections struct {
		DownloadTotal int64             `json:"downloadTotal"`
		UploadTotal   int64             `json:"uploadTotal"`
		Connections   []json.RawMessage `json:"connections"`
	}
	if err := c.decode(ctx, "/connections", &connections); err == nil {
		o.TotalDown, o.TotalUpload, o.Connections = connections.DownloadTotal, connections.UploadTotal, len(connections.Connections)
	}
	return o
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

// Proxy is a single proxy or policy group exposed by the mihomo /proxies API.
type Proxy struct {
	Type    string   `json:"type"`
	Now     string   `json:"now,omitempty"`
	All     []string `json:"all,omitempty"`
}

// proxiesResponse mirrors the shape of the mihomo /proxies response body.
type proxiesResponse struct {
	Proxies map[string]Proxy `json:"proxies"`
}

// Proxies fetches the current proxy map (including policy groups) from mihomo.
func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) {
	var out proxiesResponse
	if err := c.decode(ctx, "/proxies", &out); err != nil {
		return nil, err
	}
	return out.Proxies, nil
}

// SetMode switches the runtime mode (direct|rule|global) via PATCH /configs.
// mihomo answers with 204 No Content on success.
func (c *Client) SetMode(ctx context.Context, mode string) error {
	body, err := json.Marshal(map[string]string{"mode": mode})
	if err != nil {
		return err
	}
	return c.emptyBody(ctx, http.MethodPatch, "/configs", body)
}

// SelectProxy chooses a node within a policy group via PUT /proxies/{group}.
// mihomo answers with 204 No Content on success. The group is embedded as a
// single escaped path segment so special characters (spaces, percent, slashes,
// unicode) survive the round trip without being interpreted as separators.
func (c *Client) SelectProxy(ctx context.Context, group, name string) error {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return err
	}
	escaped := "/proxies/" + url.PathEscape(group)
	return c.emptyBodyEscaped(ctx, http.MethodPut, "/proxies/"+group, escaped, body)
}

// emptyBody performs a request that is expected to return 204 No Content and
// drains any (empty) response body.
func (c *Client) emptyBody(ctx context.Context, method, path string, body []byte) error {
	return c.emptyBodyEscaped(ctx, method, path, "", body)
}

// emptyBodyEscaped is like emptyBody but passes a pre-escaped RawPath variant.
func (c *Client) emptyBodyEscaped(ctx context.Context, method, path, rawPath string, body []byte) error {
	resp, err := c.requestEscaped(ctx, method, path, rawPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) StreamLogs(ctx context.Context, emit func([]byte) error) error {
	resp, err := c.request(ctx, http.MethodGet, "/logs?format=structured", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	s := bufio.NewScanner(resp.Body)
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		line := append([]byte(nil), s.Bytes()...)
		if !json.Valid(line) {
			line, _ = json.Marshal(map[string]string{"type": "info", "payload": string(line)})
		}
		if err := emit(line); err != nil {
			return err
		}
	}
	return s.Err()
}
