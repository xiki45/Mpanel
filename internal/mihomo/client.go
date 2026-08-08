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
	return c.decodeLimit(ctx, path, 4<<20, dst)
}

// decodeLimit is like decode but caps the response body at maxBytes.
func (c *Client) decodeLimit(ctx context.Context, path string, maxBytes int64, dst any) error {
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBytes)).Decode(dst); err != nil {
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

// Connection is a single normalized active connection returned by the mihomo
// /connections API. The fields are shaped for the panel frontend rather than
// mirroring mihomo's raw metadata structure.
type Connection struct {
	ID       string   `json:"id"`
	Host     string   `json:"host"`
	Type     string   `json:"type"`
	Network  string   `json:"network"`
	SourceIP string   `json:"sourceIP"`
	Rule     string   `json:"rule"`
	Chains   []string `json:"chains"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Start    string   `json:"start"`
}

// Connections is the normalized response of the mihomo /connections API.
// Truncated is only present when the connection list was cut at the cap.
type Connections struct {
	DownloadTotal int64        `json:"downloadTotal"`
	UploadTotal   int64        `json:"uploadTotal"`
	Connections   []Connection `json:"connections"`
	Truncated     bool         `json:"truncated,omitempty"`
}

// connectionMetadata mirrors mihomo's per-connection metadata object.
type connectionMetadata struct {
	Network         string `json:"network"`
	Type            string `json:"type"`
	SourceIP        string `json:"sourceIP"`
	DestinationIP   string `json:"destinationIP"`
	DestinationPort string `json:"destinationPort"`
	Host            string `json:"host"`
	SniffHost       string `json:"sniffHost"`
}

// rawConnection mirrors the raw shape of one item in mihomo's /connections
// response before normalization.
type rawConnection struct {
	ID          string             `json:"id"`
	Upload      int64              `json:"upload"`
	Download    int64              `json:"download"`
	Start       string             `json:"start"`
	Chains      []string           `json:"chains"`
	Rule        string             `json:"rule"`
	RulePayload string             `json:"rulePayload"`
	Metadata    connectionMetadata `json:"metadata"`
}

// rawConnections mirrors the top-level shape of mihomo's /connections response.
type rawConnections struct {
	DownloadTotal int64           `json:"downloadTotal"`
	UploadTotal   int64           `json:"uploadTotal"`
	Connections   []rawConnection `json:"connections"`
}

// maxConnections caps the number of connections returned to the panel to bound
// memory and payload size for large mihomo instances.
const maxConnections = 10000

// Connections fetches and normalizes the active connections from mihomo's
// /connections endpoint. The response body may be large, so the read limit is
// raised to 32 MiB for this endpoint only.
func (c *Client) Connections(ctx context.Context) (Connections, error) {
	var out Connections
	var raw rawConnections
	if err := c.decodeLimit(ctx, "/connections", 32<<20, &raw); err != nil {
		return out, err
	}
	out.DownloadTotal = raw.DownloadTotal
	out.UploadTotal = raw.UploadTotal
	if len(raw.Connections) > maxConnections {
		raw.Connections = raw.Connections[:maxConnections]
		out.Truncated = true
	}
	out.Connections = make([]Connection, 0, len(raw.Connections))
	for _, rc := range raw.Connections {
		out.Connections = append(out.Connections, normalizeConnection(rc))
	}
	return out, nil
}

// normalizeConnection converts a raw mihomo connection into the panel's
// normalized form, combining host/type/rule from metadata and preserving the
// original chains order.
func normalizeConnection(rc rawConnection) Connection {
	conn := Connection{
		ID:       rc.ID,
		Upload:   rc.Upload,
		Download: rc.Download,
		Start:    rc.Start,
	}
	if rc.Chains == nil {
		conn.Chains = []string{}
	} else {
		conn.Chains = rc.Chains
	}
	conn.SourceIP = rc.Metadata.SourceIP
	conn.Network = rc.Metadata.Network
	conn.Host = buildHost(rc.Metadata)
	conn.Type = buildType(rc.Metadata.Type, rc.Metadata.Network)
	conn.Rule = buildRule(rc.Rule, rc.RulePayload)
	return conn
}

// buildHost picks the best available host from a connection's metadata and
// appends the destination port when present. Host has priority, falling back to
// sniffHost and then destinationIP.
func buildHost(m connectionMetadata) string {
	host := m.Host
	if host == "" {
		host = m.SniffHost
	}
	if host == "" {
		host = m.DestinationIP
	}
	if m.DestinationPort != "" && host != "" {
		host = host + ":" + m.DestinationPort
	}
	return host
}

// buildType combines a connection's protocol type with its network, e.g.
// HTTP/TCP, keeping just the present part when either is missing.
func buildType(connType, network string) string {
	if connType != "" && network != "" {
		return connType + "/" + strings.ToUpper(network)
	}
	if connType != "" {
		return connType
	}
	return strings.ToUpper(network)
}

// buildRule formats a rule and its payload as Rule(Payload), falling back to
// just the rule when no payload is present.
func buildRule(rule, payload string) string {
	if payload != "" && rule != "" {
		return rule + "(" + payload + ")"
	}
	return rule
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
