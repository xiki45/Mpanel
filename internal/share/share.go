// Package share turns inbound listeners into standard client share links and
// their QR codes. Only protocols that map reliably are supported. If a
// supported protocol cannot produce any share link because required
// credentials or Reality fields are missing (or the Reality private key is
// invalid), a clear business error is returned rather than silently yielding an
// empty list. Unsupported protocols (mixed/http/socks/unknown) return an empty
// list with no error.
package share

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	config "mpanel/internal/config"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/curve25519"
	"gopkg.in/yaml.v3"
)

// Entry is a single share link together with its QR code data URL.
type Entry struct {
	Label     string `json:"label"`
	URI       string `json:"uri"`
	QRDataURL string `json:"qrDataUrl"`
}

// Result is the API response payload for a listener's shares.
type Result struct {
	Shares []Entry `json:"shares"`
}

// Build generates the share links for a listener addressed at the validated
// public host. Supported protocols return a clear error when they cannot
// produce any share link due to missing credentials / Reality fields or an
// invalid Reality private key. mixed/http/socks and unknown types return an
// empty, non-nil slice.
func Build(l config.Listener, host string) ([]Entry, error) {
	extra, err := parseExtra(l.ExtraYAML)
	if err != nil {
		return nil, errors.New("无法解析入站专属配置")
	}
	switch l.Type {
	case "shadowsocks":
		return buildShadowsocks(l, host, extra)
	case "vmess":
		return buildVmess(l, host, extra)
	case "vless":
		return buildVless(l, host, extra)
	case "trojan":
		return buildTrojan(l, host, extra)
	case "hysteria2":
		return buildHysteria2(l, host, extra)
	default: // mixed, http, socks, unknown -> empty shares, never fabricated
		return []Entry{}, nil
	}
}

func buildShadowsocks(l config.Listener, host string, extra map[string]any) ([]Entry, error) {
	cipher := str(extra["cipher"])
	users := list(extra["users"])
	var entries []Entry
	if len(users) > 0 {
		for _, u := range users {
			um := asMap(u)
			password := str(um["password"])
			if cipher == "" || password == "" {
				continue
			}
			label := l.Name
			if username := str(um["username"]); username != "" {
				label = l.Name + "-" + username
			}
			uri := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(cipher+":"+password)) + "@" + host + ":" + strconv.Itoa(l.Port) + "#" + url.PathEscape(label)
			entries = append(entries, mustEntry(label, uri))
		}
		if len(entries) == 0 {
			return nil, errors.New("shadowsocks 缺少加密方式或用户密码，无法生成分享链接")
		}
		return entries, nil
	}
	if password := str(extra["password"]); cipher != "" && password != "" {
		uri := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(cipher+":"+password)) + "@" + host + ":" + strconv.Itoa(l.Port) + "#" + url.PathEscape(l.Name)
		return []Entry{mustEntry(l.Name, uri)}, nil
	}
	return nil, errors.New("shadowsocks 缺少加密方式或密码，无法生成分享链接")
}

type vmessPayload struct {
	V    string `json:"v"`
	PS   string `json:"ps"`
	Add  string `json:"add"`
	Port string `json:"port"`
	ID   string `json:"id"`
	Aid  string `json:"aid"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
}

func buildVmess(l config.Listener, host string, extra map[string]any) ([]Entry, error) {
	network := str(extra["network"])
	if network == "" {
		network = "tcp"
	}
	streamType := "none"
	switch network {
	case "ws":
		streamType = "ws"
	case "grpc":
		streamType = "grpc"
	case "http":
		streamType = "http"
	}
	tlsVal := ""
	if boolValue(extra["tls"]) {
		tlsVal = "tls"
	}
	sni := str(extra["servername"])
	path := ""
	wsHost := ""
	if opts, ok := extra["ws-opts"].(map[string]any); ok {
		if p := str(opts["path"]); p != "" {
			path = p
		}
		if hdrs, ok := opts["headers"].(map[string]any); ok {
			wsHost = str(hdrs["Host"])
		}
	}

	users := list(extra["users"])
	if len(users) == 0 {
		return nil, errors.New("vmess 缺少用户，无法生成分享链接")
	}
	var entries []Entry
	for _, u := range users {
		um := asMap(u)
		id := str(um["uuid"])
		if id == "" {
			continue
		}
		aid := str(um["alterId"])
		if aid == "" {
			aid = "0"
		}
		label := l.Name
		if username := str(um["username"]); username != "" {
			label = l.Name + "-" + username
		}
		payload := vmessPayload{
			V: "2", PS: label, Add: host, Port: strconv.Itoa(l.Port),
			ID: id, Aid: aid, Net: network, Type: streamType,
			Host: wsHost, Path: path, TLS: tlsVal, SNI: sni,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		uri := "vmess://" + base64.StdEncoding.EncodeToString(raw)
		entries = append(entries, mustEntry(label, uri))
	}
	if len(entries) == 0 {
		return nil, errors.New("vmess 缺少用户 UUID，无法生成分享链接")
	}
	return entries, nil
}

// buildVless produces VLESS share links. For Reality, the client URI needs only
// a valid private key (from which the public key is derived), a server name and
// a short id; `dest` is not part of the client link so it is not required here.
// Config validity (which does require dest) is enforced by the config layer's
// validateListener. An invalid Reality private key is reported as an explicit
// error and never included in the message.
func buildVless(l config.Listener, host string, extra map[string]any) ([]Entry, error) {
	if l.Vless == nil {
		return nil, errors.New("VLESS 缺少用户，无法生成分享链接")
	}
	network := str(extra["network"])
	if network == "" {
		network = "tcp"
	}
	tlsEnabled := boolValue(extra["tls"])
	sni := str(extra["servername"])

	var entries []Entry
	for _, u := range l.Vless.Users {
		if u.UUID == "" {
			continue
		}
		label := l.Name
		if u.Username != "" {
			label = l.Name + "-" + u.Username
		}
		params := "encryption=none"
		if l.Vless.Reality != nil {
			r := l.Vless.Reality
			if r.PrivateKey == "" || len(r.ServerNames) == 0 || len(r.ShortIds) == 0 {
				return nil, errors.New("Reality 缺少 private-key、server-names 或 short-id，无法生成分享链接")
			}
			pbk, err := derivePublicKey(r.PrivateKey)
			if err != nil {
				return nil, errors.New("Reality 私钥无效，无法生成分享链接")
			}
			params += "&security=reality"
			params += "&sni=" + url.QueryEscape(r.ServerNames[0])
			params += "&fp=chrome"
			params += "&pbk=" + url.QueryEscape(pbk)
			params += "&sid=" + url.QueryEscape(r.ShortIds[0])
			params += "&type=tcp"
		} else if tlsEnabled {
			params += "&security=tls"
			if sni != "" {
				params += "&sni=" + url.QueryEscape(sni)
			}
			params += "&type=" + url.QueryEscape(network)
		} else {
			params += "&security=none&type=" + url.QueryEscape(network)
		}
		if u.Flow != "" {
			params += "&flow=" + url.QueryEscape(u.Flow)
		}
		uri := "vless://" + url.QueryEscape(u.UUID) + "@" + host + ":" + strconv.Itoa(l.Port) + "?" + params + "#" + url.PathEscape(label)
		entries = append(entries, mustEntry(label, uri))
	}
	if len(entries) == 0 {
		return nil, errors.New("VLESS 缺少有效的用户 UUID，无法生成分享链接")
	}
	return entries, nil
}

func buildTrojan(l config.Listener, host string, extra map[string]any) ([]Entry, error) {
	users := list(extra["users"])
	var entries []Entry
	if len(users) > 0 {
		for _, u := range users {
			um := asMap(u)
			password := str(um["password"])
			if password == "" {
				continue
			}
			label := l.Name
			if username := str(um["username"]); username != "" {
				label = l.Name + "-" + username
			}
			uri := "trojan://" + url.QueryEscape(password) + "@" + host + ":" + strconv.Itoa(l.Port) + "?security=tls&type=tcp#" + url.PathEscape(label)
			entries = append(entries, mustEntry(label, uri))
		}
		if len(entries) == 0 {
			return nil, errors.New("trojan 缺少用户密码，无法生成分享链接")
		}
		return entries, nil
	}
	if password := str(extra["password"]); password != "" {
		uri := "trojan://" + url.QueryEscape(password) + "@" + host + ":" + strconv.Itoa(l.Port) + "?security=tls&type=tcp#" + url.PathEscape(l.Name)
		return []Entry{mustEntry(l.Name, uri)}, nil
	}
	return nil, errors.New("trojan 缺少密码，无法生成分享链接")
}

func buildHysteria2(l config.Listener, host string, extra map[string]any) ([]Entry, error) {
	var params []string
	if sni := str(extra["sni"]); sni != "" {
		params = append(params, "sni="+url.QueryEscape(sni))
	}
	if boolValue(extra["insecure"]) {
		params = append(params, "insecure=1")
	}
	query := joinParams(params...)

	if password := str(extra["password"]); password != "" {
		uri := "hysteria2://" + url.QueryEscape(password) + "@" + host + ":" + strconv.Itoa(l.Port) + query + "#" + url.PathEscape(l.Name)
		return []Entry{mustEntry(l.Name, uri)}, nil
	}
	var entries []Entry
	for _, u := range list(extra["users"]) {
		um := asMap(u)
		p := str(um["password"])
		if p == "" {
			continue
		}
		label := l.Name
		if username := str(um["username"]); username != "" {
			label = l.Name + "-" + username
		}
		uri := "hysteria2://" + url.QueryEscape(p) + "@" + host + ":" + strconv.Itoa(l.Port) + query + "#" + url.PathEscape(label)
		entries = append(entries, mustEntry(label, uri))
	}
	if len(entries) == 0 {
		return nil, errors.New("hysteria2 缺少密码，无法生成分享链接")
	}
	return entries, nil
}

// joinParams builds a "?a=1&b=2" suffix from a variadic list of "k=v" or "k"
// fragments. Empty fragments are skipped.
func joinParams(fragments ...string) string {
	var parts []string
	for _, f := range fragments {
		if f != "" {
			parts = append(parts, f)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "?" + strings.Join(parts, "&")
}

// derivePublicKey derives the X25519 public key (base64 URL-safe, no padding)
// from a Reality private key. Both the URL-safe and standard base64 encodings
// are accepted for the private key.
func derivePublicKey(privateB64 string) (string, error) {
	priv, err := decodeRealityKey(privateB64)
	if err != nil {
		return "", err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(pub), nil
}

func decodeRealityKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("invalid reality private key")
}

// ValidateHost validates a public host/IP supplied by the caller and returns it
// in URI-ready form (IPv6 addresses wrapped in square brackets). It rejects
// scheme, path, query, userinfo and port injection as well as anything that is
// not a valid DNS name, IPv4 or IPv6 address.
func ValidateHost(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("host 必填")
	}
	if strings.ContainsAny(s, "/?#@") {
		return "", errors.New("host 包含非法字符")
	}

	// Bracketed IPv6 literal.
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", errors.New("host 无效")
		}
		inner := s[1:end]
		ip := net.ParseIP(inner)
		if ip == nil || ip.To4() != nil {
			return "", errors.New("host 无效")
		}
		if strings.TrimSpace(s[end+1:]) != "" {
			return "", errors.New("host 不能包含端口")
		}
		return "[" + ip.String() + "]", nil
	}

	// Plain IP (IPv4 or IPv6); IPv6 is wrapped in brackets for the URI.
	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() != nil {
			return ip.String(), nil
		}
		return "[" + ip.String() + "]", nil
	}

	// Not an IP: a colon here means host:port injection.
	if strings.Contains(s, ":") {
		return "", errors.New("host 不能包含端口")
	}
	if !isHostname(s) {
		return "", errors.New("host 无效")
	}
	return s, nil
}

func isHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	// Labels are dot-separated, non-empty, and must not start or end with a
	// hyphen.
	labelStart := true
	labelLen := 0
	prevHyphen := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			if labelStart || prevHyphen {
				return false
			}
			labelStart = true
			labelLen = 0
			prevHyphen = false
		case isAlphaNumeric(c):
			labelStart = false
			labelLen++
			prevHyphen = false
			if labelLen > 63 {
				return false
			}
		case c == '-':
			if labelStart {
				return false
			}
			labelLen++
			prevHyphen = true
			if labelLen > 63 {
				return false
			}
		default:
			return false
		}
	}
	return !labelStart && !prevHyphen
}

func isAlphaNumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func mustEntry(label, uri string) Entry {
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		// URI was built from validated, escape-safe inputs; encoding failure
		// here is unexpected, so leave a harmless empty PNG data URL.
		return Entry{Label: label, URI: uri, QRDataURL: "data:image/png;base64,"}
	}
	return Entry{Label: label, URI: uri, QRDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}
}

func parseExtra(extraYAML string) (map[string]any, error) {
	m := map[string]any{}
	if strings.TrimSpace(extraYAML) == "" {
		return m, nil
	}
	if err := yaml.Unmarshal([]byte(extraYAML), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func boolValue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	default:
		return false
	}
}

func list(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
