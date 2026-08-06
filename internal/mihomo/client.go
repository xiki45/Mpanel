package mihomo

import (
	"bufio"
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
	pathOnly, query := path, ""
	if i := strings.IndexByte(path, '?'); i >= 0 {
		pathOnly, query = path[:i], path[i+1:]
	}
	u := c.base.ResolveReference(&url.URL{Path: pathOnly, RawQuery: query})
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
	if err := c.decode(ctx, "/traffic", &traffic); err == nil {
		o.Upload, o.Download = traffic.Up, traffic.Down
	}
	var memory struct {
		Inuse int64 `json:"inuse"`
	}
	if err := c.decode(ctx, "/memory", &memory); err == nil {
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
