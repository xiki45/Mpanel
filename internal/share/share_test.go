package share

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	config "mpanel/internal/config"
)

const (
	testPrivateRawURL = "ejvNUybzw41Ku-82lY1vj5GDW4w_JkpwU7b839vny0g"
	testPrivateStd    = "ejvNUybzw41Ku+82lY1vj5GDW4w/JkpwU7b839vny0g="
	testPublicRawURL  = "lin1PN5EcW8WOXzlC5rh1ip7ZJWZjN8tpYHGBU1pED0"
	testUUID          = "11111111-1111-1111-1111-111111111111"
)

func TestDerivePublicKey(t *testing.T) {
	pub, err := derivePublicKey(testPrivateRawURL)
	if err != nil {
		t.Fatal(err)
	}
	if pub != testPublicRawURL {
		t.Fatalf("public key mismatch: got %q want %q", pub, testPublicRawURL)
	}
	// The standard base64 encoding must also be accepted.
	pub2, err := derivePublicKey(testPrivateStd)
	if err != nil {
		t.Fatal(err)
	}
	if pub2 != testPublicRawURL {
		t.Fatalf("standard-encoded key public mismatch: %q", pub2)
	}
	if _, err := derivePublicKey("not-a-valid-key"); err == nil {
		t.Fatal("invalid private key accepted")
	}
}

func TestVlessRealityURI(t *testing.T) {
	l := config.Listener{
		Name: "vless-node", Type: "vless", Listen: "0.0.0.0", Port: 443,
		Vless: &config.Vless{
			Users: []config.VlessUser{{Username: "alice", UUID: testUUID, Flow: "xtls-rprx-vision"}},
			Reality: &config.Reality{
				Dest: "example.com:443", PrivateKey: testPrivateRawURL,
				ShortIds: []string{"123456"}, ServerNames: []string{"example.com"},
			},
		},
	}
	entries, err := Build(l, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := "vless://" + testUUID + "@example.com:443?encryption=none&security=reality&sni=example.com&fp=chrome&pbk=" + testPublicRawURL + "&sid=123456&type=tcp&flow=xtls-rprx-vision#vless-node-alice"
	if entries[0].URI != want {
		t.Fatalf("URI mismatch:\n got %s\nwant %s", entries[0].URI, want)
	}
	if strings.Contains(entries[0].URI, testPrivateRawURL) || strings.Contains(entries[0].URI, testPrivateStd) {
		t.Fatal("private key leaked into share URI")
	}
	if !strings.HasPrefix(entries[0].QRDataURL, "data:image/png;base64,") {
		t.Fatalf("bad qr data url prefix: %s", entries[0].QRDataURL)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(entries[0].QRDataURL, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if !isPNG(png) {
		t.Fatal("qr data url does not decode to a PNG")
	}
}

func TestVlessRealityInsufficientFieldsReturnsError(t *testing.T) {
	l := config.Listener{
		Name: "node", Type: "vless", Listen: "0.0.0.0", Port: 443,
		Vless: &config.Vless{
			Users: []config.VlessUser{
				{Username: "ok", UUID: testUUID},
			},
			Reality: &config.Reality{
				PrivateKey: testPrivateRawURL, ServerNames: []string{"example.com"},
				// missing ShortIds -> cannot form a Reality URI
			},
		},
	}
	if _, err := Build(l, "example.com"); err == nil {
		t.Fatal("expected an error for insufficient Reality fields")
	} else if !strings.Contains(err.Error(), "Reality") {
		t.Fatalf("expected a clear Reality error, got: %v", err)
	}
}

func TestVlessRealityInvalidPrivateKeyReturnsClearError(t *testing.T) {
	l := config.Listener{
		Name: "node", Type: "vless", Listen: "0.0.0.0", Port: 443,
		Vless: &config.Vless{
			Users: []config.VlessUser{{Username: "ok", UUID: testUUID}},
			Reality: &config.Reality{
				PrivateKey: "not-a-valid-base64-key!!", ShortIds: []string{"123456"}, ServerNames: []string{"example.com"},
			},
		},
	}
	_, err := Build(l, "example.com")
	if err == nil {
		t.Fatal("expected an error for invalid Reality private key")
	}
	msg := err.Error()
	if !strings.Contains(msg, "私钥无效") {
		t.Fatalf("expected clear private-key error, got: %v", msg)
	}
	if strings.Contains(msg, "not-a-valid-base64-key") {
		t.Fatalf("private key leaked into error message: %v", msg)
	}
}

// TestVlessRealityDoesNotRequireDest documents the chosen strategy: the client
// share URI has no place for `dest`, so share generation does not require it.
// Service-config validity (which does require dest) is enforced separately by
// the config layer's validateVless.
func TestVlessRealityDoesNotRequireDest(t *testing.T) {
	l := config.Listener{
		Name: "node", Type: "vless", Listen: "0.0.0.0", Port: 443,
		Vless: &config.Vless{
			Users: []config.VlessUser{{Username: "ok", UUID: testUUID}},
			Reality: &config.Reality{
				PrivateKey: testPrivateRawURL, ShortIds: []string{"123456"}, ServerNames: []string{"example.com"},
				// no Dest -> still a valid client URI
			},
		},
	}
	entries, err := Build(l, "example.com")
	if err != nil {
		t.Fatalf("dest should not be required for URI generation: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].URI, "security=reality") {
		t.Fatalf("reality URI without dest wrong: %+v", entries)
	}
}

func TestVlessMultiUserPerUser(t *testing.T) {
	l := config.Listener{
		Name: "node", Type: "vless", Listen: "0.0.0.0", Port: 443,
		Vless: &config.Vless{
			Users: []config.VlessUser{
				{Username: "alice", UUID: testUUID},
				{Username: "bob", UUID: "22222222-2222-2222-2222-222222222222"},
			},
			Reality: &config.Reality{PrivateKey: testPrivateRawURL, ShortIds: []string{"123456"}, ServerNames: []string{"example.com"}},
		},
	}
	entries, err := Build(l, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if strings.Contains(entries[0].URI, entries[1].URI) && entries[0].URI != entries[1].URI {
		t.Fatal("unexpected overlap")
	}
	if !strings.Contains(entries[0].URI, "11111111") || !strings.Contains(entries[1].URI, "22222222") {
		t.Fatalf("user uuids not mapped per entry: %s / %s", entries[0].URI, entries[1].URI)
	}
}

func TestNonMappableProtocolsReturnEmpty(t *testing.T) {
	for _, typ := range []string{"mixed", "http", "socks"} {
		l := config.Listener{Name: "n", Type: typ, Listen: "0.0.0.0", Port: 1234, ExtraYAML: "password: x"}
		entries, err := Build(l, "example.com")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", typ, err)
		}
		if len(entries) != 0 {
			t.Fatalf("%s: expected empty shares, got %d", typ, len(entries))
		}
	}
}

func TestShadowsocksURI(t *testing.T) {
	l := config.Listener{Name: "ss-node", Type: "shadowsocks", Listen: "0.0.0.0", Port: 8388, ExtraYAML: "cipher: aes-128-gcm\npassword: s3cret p@ss\n"}
	entries, err := Build(l, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:s3cret p@ss")) + "@1.2.3.4:8388#" + url.PathEscape("ss-node")
	if entries[0].URI != want {
		t.Fatalf("ss URI mismatch:\n got %s\nwant %s", entries[0].URI, want)
	}
}

func TestShadowsocksMultiUser(t *testing.T) {
	l := config.Listener{
		Name: "ss-node", Type: "shadowsocks", Listen: "0.0.0.0", Port: 8388,
		ExtraYAML: "cipher: aes-128-gcm\nusers:\n  - username: a\n    password: p1\n  - username: b\n    password: p2\n",
	}
	entries, err := Build(l, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	want1 := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p1"))
	want2 := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p2"))
	if !strings.Contains(entries[0].URI, want1) || !strings.Contains(entries[1].URI, want2) {
		t.Fatalf("per-user ss credentials missing: %s / %s", entries[0].URI, entries[1].URI)
	}
	if entries[0].URI == entries[1].URI {
		t.Fatal("per-user ss URIs are identical")
	}
}

func TestSupportedProtocolsMissingCredentialsReturnError(t *testing.T) {
	cases := []struct {
		name     string
		listener config.Listener
	}{
		{"shadowsocks", config.Listener{Name: "n", Type: "shadowsocks", Listen: "0.0.0.0", Port: 8388}},
		{"vmess", config.Listener{Name: "n", Type: "vmess", Listen: "0.0.0.0", Port: 10086}},
		{"vless", config.Listener{Name: "n", Type: "vless", Listen: "0.0.0.0", Port: 443}},
		{"trojan", config.Listener{Name: "n", Type: "trojan", Listen: "0.0.0.0", Port: 443}},
		{"hysteria2", config.Listener{Name: "n", Type: "hysteria2", Listen: "0.0.0.0", Port: 443}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := Build(tc.listener, "example.com")
			if err == nil {
				t.Fatalf("expected an error for missing credentials, got entries=%d", len(entries))
			}
			if len(entries) != 0 {
				t.Fatalf("expected no entries on error, got %d", len(entries))
			}
		})
	}
}

func TestShadowsocksMissingPasswordReturnsError(t *testing.T) {
	// users present but none have a password.
	l := config.Listener{Name: "n", Type: "shadowsocks", Listen: "0.0.0.0", Port: 8388, ExtraYAML: "cipher: aes-128-gcm\nusers:\n  - username: a\n  - username: b\n"}
	if _, err := Build(l, "example.com"); err == nil {
		t.Fatal("expected error when all ss users lack a password")
	}
}

func TestValidateHost(t *testing.T) {
	for _, h := range []string{"example.com", "a.b.example.com", "1.2.3.4", "10.0.0.1", "2001:db8::1", "[2001:db8::1]"} {
		got, err := ValidateHost(h)
		if err != nil {
			t.Errorf("ValidateHost(%q) error: %v", h, err)
			continue
		}
		if strings.Contains(got, "::") && !strings.HasPrefix(got, "[") {
			t.Errorf("IPv6 %q not bracketed: %q", h, got)
		}
	}
	for _, h := range []string{
		"", " ", "http://example.com", "https://example.com", "example.com/path",
		"example.com?x=1", "user@example.com", "example.com:443", "1.2.3.4:80",
		"[::1]:443", "bad_host", "-bad.example", "bad-.example", "example..com",
	} {
		if _, err := ValidateHost(h); err == nil {
			t.Errorf("ValidateHost(%q) expected error", h)
		}
	}
}

func TestValidateHostBracketsIPv6InOutput(t *testing.T) {
	got, err := ValidateHost("2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "[2001:db8::1]" {
		t.Fatalf("got %q, want [2001:db8::1]", got)
	}
}

func TestTrojanAndHysteria2URIs(t *testing.T) {
	tj := config.Listener{Name: "tj", Type: "trojan", Listen: "0.0.0.0", Port: 443, ExtraYAML: "password: tjpass\n"}
	entries, err := Build(tj, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].URI, "trojan://tjpass@example.com:443?security=tls&type=tcp#") {
		t.Fatalf("trojan URI wrong: %+v", entries)
	}

	hy := config.Listener{Name: "hy", Type: "hysteria2", Listen: "0.0.0.0", Port: 443, ExtraYAML: "password: hpass\nsni: cdn.example.com\n"}
	entries, err = Build(hy, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].URI, "hysteria2://hpass@example.com:443?sni=cdn.example.com#") {
		t.Fatalf("hysteria2 URI wrong: %+v", entries)
	}
}

func isPNG(p []byte) bool {
	// PNG signature: 89 50 4E 47 0D 0A 1A 0A
	return len(p) >= 8 && p[0] == 0x89 && p[1] == 'P' && p[2] == 'N' && p[3] == 'G' &&
		p[4] == 0x0D && p[5] == 0x0A && p[6] == 0x1A && p[7] == 0x0A
}
