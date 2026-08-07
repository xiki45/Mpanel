package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeValidator struct {
	err   error
	calls int
}

func (f *fakeValidator) Validate(context.Context, string) error { f.calls++; return f.err }

type fakeReloader struct {
	errors []error
	calls  int
}

type blockingValidator struct {
	entered chan struct{}
	release chan struct{}
}

func (v *blockingValidator) Validate(context.Context, string) error {
	v.entered <- struct{}{}
	<-v.release
	return nil
}

type contextReloader struct {
	mu           sync.Mutex
	errors       []error
	hasDeadlines []bool
}

type rollbackReloader struct {
	calls int
}

func (r *rollbackReloader) Reload(context.Context) error {
	r.calls++
	if r.calls%2 == 1 {
		return errors.New("reload failed")
	}
	return nil
}

func (r *contextReloader) Reload(ctx context.Context) error {
	r.mu.Lock()
	_, hasDeadline := ctx.Deadline()
	r.errors = append(r.errors, ctx.Err())
	r.hasDeadlines = append(r.hasDeadlines, hasDeadline)
	call := len(r.errors)
	r.mu.Unlock()
	if call == 1 {
		return ctx.Err()
	}
	return nil
}

func (f *fakeReloader) Reload(context.Context) error {
	f.calls++
	if len(f.errors) >= f.calls {
		return f.errors[f.calls-1]
	}
	return nil
}

func newManager(t *testing.T, content string, v *fakeValidator, r *fakeReloader) *Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return &Manager{Path: path, Validator: v, Reloader: r}
}

func TestValidationFailureDoesNotOverwrite(t *testing.T) {
	v := &fakeValidator{err: errors.New("invalid")}
	m := newManager(t, "mode: rule\n", v, &fakeReloader{})
	if m.Save(context.Background(), []byte("mode: global\n")) == nil {
		t.Fatal("expected failure")
	}
	got, _ := os.ReadFile(m.Path)
	if string(got) != "mode: rule\n" {
		t.Fatalf("file changed: %q", got)
	}
	backups, _ := m.Backups()
	if len(backups) != 0 {
		t.Fatal("backup created before validation")
	}
}

func TestSuccessfulSaveCreatesBackup(t *testing.T) {
	m := newManager(t, "mode: rule\n", &fakeValidator{}, &fakeReloader{})
	if err := m.Save(context.Background(), []byte("mode: global\n")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(m.Path)
	if string(got) != "mode: global\n" {
		t.Fatalf("wrong content %q", got)
	}
	backups, _ := m.Backups()
	if len(backups) != 1 {
		t.Fatalf("expected backup, got %d", len(backups))
	}
}

func TestReloadFailureRollsBack(t *testing.T) {
	r := &fakeReloader{errors: []error{errors.New("down"), nil}}
	m := newManager(t, "mode: rule\n", &fakeValidator{}, r)
	if m.Save(context.Background(), []byte("mode: global\n")) == nil {
		t.Fatal("expected reload failure")
	}
	got, _ := os.ReadFile(m.Path)
	if string(got) != "mode: rule\n" {
		t.Fatalf("rollback failed: %q", got)
	}
	if r.calls != 2 {
		t.Fatalf("expected reload retry, got %d", r.calls)
	}
}

func TestCanceledReloadUsesIndependentBoundedCompensationContext(t *testing.T) {
	r := &contextReloader{}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		Path:                path,
		Validator:           &fakeValidator{},
		Reloader:            r,
		CompensationTimeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.Save(ctx, []byte("mode: global\n"))
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("expected original cancellation in error, got %v", err)
	}
	r.mu.Lock()
	contextErrors := append([]error(nil), r.errors...)
	hasDeadlines := append([]bool(nil), r.hasDeadlines...)
	r.mu.Unlock()
	if len(contextErrors) != 2 {
		t.Fatalf("expected initial and compensation reloads, got %d", len(contextErrors))
	}
	if !errors.Is(contextErrors[0], context.Canceled) {
		t.Fatalf("initial reload did not receive canceled request context: %v", contextErrors[0])
	}
	if contextErrors[1] != nil {
		t.Fatalf("compensation context was already canceled: %v", contextErrors[1])
	}
	if !hasDeadlines[1] {
		t.Fatal("compensation context has no deadline")
	}
	got, _ := os.ReadFile(m.Path)
	if string(got) != "mode: rule\n" {
		t.Fatalf("rollback failed: %q", got)
	}
}

func TestListenerCRUDPreservesUnknownFields(t *testing.T) {
	initial := "mode: rule\nunknown-top:\n  nested: yes\nlisteners:\n  - name: first\n    type: mixed\n    listen: 127.0.0.1\n    port: 7890\n    custom-option: keep\n"
	m := newManager(t, initial, &fakeValidator{}, &fakeReloader{})
	ctx := context.Background()
	items, err := m.Listeners()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateListener(ctx, "first", Listener{Name: "renamed", Type: "http", Listen: "0.0.0.0", Port: 8080, ExtraYAML: items[0].ExtraYAML + "\nnew-option: value"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.Path)
	text := string(data)
	for _, want := range []string{"unknown-top:", "nested: yes", "custom-option: keep", "new-option: value", "name: renamed"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if err := m.CreateListener(ctx, Listener{Name: "renamed", Type: "socks", Listen: "127.0.0.1", Port: 1080}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := m.CreateListener(ctx, Listener{Name: "bad", Type: "socks", Listen: "127.0.0.1", Port: 70000}); err == nil {
		t.Fatal("invalid port accepted")
	}
	if err := m.DeleteListener(ctx, "renamed"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(m.Path)
	if strings.Contains(string(data), "name: renamed") {
		t.Fatal("listener not deleted")
	}
	if !strings.Contains(string(data), "unknown-top:") {
		t.Fatal("top-level unknown field lost")
	}
}

func TestListenerUpdatePreciselyReplacesExtraYAML(t *testing.T) {
	initial := "mode: rule\nlisteners:\n  - name: first\n    type: mixed\n    listen: 127.0.0.1\n    port: 7890\n    keep-option: keep\n    remove-option: remove\n"
	m := newManager(t, initial, &fakeValidator{}, &fakeReloader{})
	listener := Listener{Name: "first", Type: "mixed", Listen: "127.0.0.1", Port: 7890, ExtraYAML: "keep-option: keep"}
	if err := m.UpdateListener(context.Background(), "first", listener); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.Path)
	if !strings.Contains(string(data), "keep-option: keep") {
		t.Fatalf("submitted extra field was lost:\n%s", data)
	}
	if strings.Contains(string(data), "remove-option") {
		t.Fatalf("deleted extra field was retained:\n%s", data)
	}

	listener.ExtraYAML = ""
	if err := m.UpdateListener(context.Background(), "first", listener); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(m.Path)
	if strings.Contains(string(data), "keep-option") || strings.Contains(string(data), "remove-option") {
		t.Fatalf("cleared extra YAML fields were retained:\n%s", data)
	}
	for _, common := range []string{"name: first", "type: mixed", "listen: 127.0.0.1", "port: 7890"} {
		if !strings.Contains(string(data), common) {
			t.Fatalf("common field %q was lost:\n%s", common, data)
		}
	}
}

func TestConcurrentListenerTransactionsAreSerializedWithoutLostUpdate(t *testing.T) {
	validator := &blockingValidator{entered: make(chan struct{}), release: make(chan struct{})}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\nlisteners: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Path: path, Validator: validator, Reloader: &fakeReloader{}}
	results := make(chan error, 2)
	go func() {
		results <- m.CreateListener(context.Background(), Listener{Name: "first", Type: "socks", Listen: "127.0.0.1", Port: 1080})
	}()
	<-validator.entered
	go func() {
		results <- m.CreateListener(context.Background(), Listener{Name: "second", Type: "http", Listen: "127.0.0.1", Port: 8080})
	}()
	select {
	case <-validator.entered:
		t.Fatal("second transaction entered validation before first transaction completed")
	case <-time.After(100 * time.Millisecond):
	}
	validator.release <- struct{}{}
	select {
	case <-validator.entered:
	case <-time.After(time.Second):
		t.Fatal("second transaction did not start after first transaction completed")
	}
	validator.release <- struct{}{}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	listeners, err := m.Listeners()
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 2 || listeners[0].Name != "first" || listeners[1].Name != "second" {
		t.Fatalf("concurrent update was lost: %+v", listeners)
	}
}

func TestBackupNamesDoNotCollideAtSameTimestamp(t *testing.T) {
	m := newManager(t, "mode: rule\n", &fakeValidator{}, &fakeReloader{})
	fixed := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	m.Now = func() time.Time { return fixed }
	if err := m.Save(context.Background(), []byte("mode: global\n")); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(context.Background(), []byte("mode: direct\n")); err != nil {
		t.Fatal(err)
	}
	backups, err := m.Backups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 || backups[0].ID == backups[1].ID {
		t.Fatalf("expected two distinct backups, got %+v", backups)
	}
}

func TestAtomicSavePreservesFileMetadata(t *testing.T) {
	m := newManager(t, "mode: rule\n", &fakeValidator{}, &fakeReloader{})
	if err := os.Chmod(m.Path, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Save(context.Background(), []byte("mode: global\n")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("mode changed from %v to %v", before.Mode().Perm(), after.Mode().Perm())
	}
	beforeStat, beforeOK := before.Sys().(*syscall.Stat_t)
	afterStat, afterOK := after.Sys().(*syscall.Stat_t)
	if !beforeOK || !afterOK {
		t.Fatal("platform does not expose file ownership metadata")
	}
	if beforeStat.Uid != afterStat.Uid || beforeStat.Gid != afterStat.Gid {
		t.Fatalf("ownership changed from %d:%d to %d:%d", beforeStat.Uid, beforeStat.Gid, afterStat.Uid, afterStat.Gid)
	}
}

func TestAtomicRollbackPreservesFileMetadata(t *testing.T) {
	reloader := &fakeReloader{errors: []error{errors.New("down"), nil}}
	m := newManager(t, "mode: rule\n", &fakeValidator{}, reloader)
	if err := os.Chmod(m.Path, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Save(context.Background(), []byte("mode: global\n")); err == nil {
		t.Fatal("expected reload failure")
	}
	after, err := os.Stat(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("mode changed from %v to %v", before.Mode().Perm(), after.Mode().Perm())
	}
	beforeStat, beforeOK := before.Sys().(*syscall.Stat_t)
	afterStat, afterOK := after.Sys().(*syscall.Stat_t)
	if !beforeOK || !afterOK {
		t.Fatal("platform does not expose file ownership metadata")
	}
	if beforeStat.Uid != afterStat.Uid || beforeStat.Gid != afterStat.Gid {
		t.Fatalf("ownership changed from %d:%d to %d:%d", beforeStat.Uid, beforeStat.Gid, afterStat.Uid, afterStat.Gid)
	}
}

func TestValidateListenerVless(t *testing.T) {
	ctx := context.Background()
	newBase := func() *Manager {
		m := newManager(t, "mode: rule\nlisteners: []\n", &fakeValidator{}, &fakeReloader{})
		return m
	}

	base := func() Listener {
		return Listener{
			Name: "v1", Type: "vless", Listen: "0.0.0.0", Port: 443,
			Vless: &Vless{Users: []VlessUser{{Username: "u", UUID: "11111111-1111-1111-1111-111111111111"}}},
		}
	}

	t.Run("no users", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless.Users = nil
		if err := m.CreateListener(ctx, l); err == nil {
			t.Fatal("expected error for vless with no users")
		}
	})
	t.Run("empty uuid", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless.Users = []VlessUser{{Username: "u", UUID: ""}}
		if err := m.CreateListener(ctx, l); err == nil {
			t.Fatal("expected error for vless user with empty uuid")
		}
	})
	t.Run("reality missing dest", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless.Reality = &Reality{PrivateKey: "k", ShortIds: []string{"a"}, ServerNames: []string{"example.com"}}
		if err := m.CreateListener(ctx, l); err == nil {
			t.Fatal("expected error for reality missing dest")
		}
	})
	t.Run("reality missing private-key", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless.Reality = &Reality{Dest: "example.com:443", ShortIds: []string{"a"}, ServerNames: []string{"example.com"}}
		if err := m.CreateListener(ctx, l); err == nil {
			t.Fatal("expected error for reality missing private-key")
		}
	})
	t.Run("reality missing short-id", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless.Reality = &Reality{Dest: "example.com:443", PrivateKey: "k", ServerNames: []string{"example.com"}}
		if err := m.CreateListener(ctx, l); err == nil {
			t.Fatal("expected error for reality missing short-id")
		}
	})
	t.Run("reality missing server-names", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless.Reality = &Reality{Dest: "example.com:443", PrivateKey: "k", ShortIds: []string{"a"}}
		if err := m.CreateListener(ctx, l); err == nil {
			t.Fatal("expected error for reality missing server-names")
		}
	})
	t.Run("valid vless accepted", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless.Reality = &Reality{Dest: "example.com:443", PrivateKey: "k", ShortIds: []string{"a"}, ServerNames: []string{"example.com"}}
		if err := m.CreateListener(ctx, l); err != nil {
			t.Fatalf("valid vless rejected: %v", err)
		}
	})
	t.Run("vless without Vless struct allowed", func(t *testing.T) {
		m := newBase()
		l := base()
		l.Vless = nil
		if err := m.CreateListener(ctx, l); err != nil {
			t.Fatalf("vless without Vless struct should be allowed: %v", err)
		}
	})
}

func TestListenersReadDoesNotValidateVless(t *testing.T) {
	// Historical config with an incomplete VLESS block must still be readable
	// (read-only path bypasses validateListener).
	initial := "mode: rule\nlisteners:\n  - name: v\n    type: vless\n    listen: 0.0.0.0\n    port: 443\n    users: []\n"
	m := newManager(t, initial, &fakeValidator{}, &fakeReloader{})
	if _, err := m.Listeners(); err != nil {
		t.Fatalf("read-only Listeners must not validate vless: %v", err)
	}
}

const vlessRealityInitial = `mode: rule
listeners:
  - name: vless-r
    type: vless
    listen: 0.0.0.0
    port: 443
    network: tcp
    users:
      - username: alice
        uuid: 11111111-1111-1111-1111-111111111111
        flow: xtls-rprx-vision
        client-fingerprint: chrome
      - username: bob
        uuid: 22222222-2222-2222-2222-222222222222
    reality-config:
      dest: example.com:443
      private-key: abcdefprivatekey
      short-id:
        - 123456
        - abcdef
      server-names:
        - example.com
      show: true
      min-ver: 1.2
`

func TestVlessListenerReadsStructuredFields(t *testing.T) {
	m := newManager(t, vlessRealityInitial, &fakeValidator{}, &fakeReloader{})
	items, err := m.Listeners()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 listener, got %d", len(items))
	}
	l := items[0]
	if l.Vless == nil {
		t.Fatal("vless field not parsed")
	}
	if len(l.Vless.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(l.Vless.Users))
	}
	if l.Vless.Users[0].Username != "alice" || l.Vless.Users[0].UUID != "11111111-1111-1111-1111-111111111111" || l.Vless.Users[0].Flow != "xtls-rprx-vision" {
		t.Fatalf("first user wrong: %+v", l.Vless.Users[0])
	}
	if !strings.Contains(l.Vless.Users[0].ExtraYAML, "client-fingerprint") {
		t.Fatalf("unknown user field lost: %q", l.Vless.Users[0].ExtraYAML)
	}
	r := l.Vless.Reality
	if r == nil {
		t.Fatal("reality not parsed")
	}
	if r.Dest != "example.com:443" || r.PrivateKey != "abcdefprivatekey" {
		t.Fatalf("reality scalar fields wrong: %+v", r)
	}
	if len(r.ShortIds) != 2 || r.ShortIds[0] != "123456" || r.ShortIds[1] != "abcdef" {
		t.Fatalf("short-ids wrong: %+v", r.ShortIds)
	}
	if len(r.ServerNames) != 1 || r.ServerNames[0] != "example.com" {
		t.Fatalf("server-names wrong: %+v", r.ServerNames)
	}
	if !strings.Contains(r.ExtraYAML, "show: true") || !strings.Contains(r.ExtraYAML, "min-ver:") {
		t.Fatalf("unknown reality fields lost: %q", r.ExtraYAML)
	}
	if !strings.Contains(l.ExtraYAML, "network:") {
		t.Fatalf("listener-specific field network was not kept in ExtraYAML: %q", l.ExtraYAML)
	}
	if strings.Contains(l.ExtraYAML, "reality-config") || strings.Contains(l.ExtraYAML, "users:") {
		t.Fatalf("structured vless fields leaked into ExtraYAML: %q", l.ExtraYAML)
	}
}

func TestVlessRealityRoundTripPreservesUnknownFields(t *testing.T) {
	m := newManager(t, vlessRealityInitial, &fakeValidator{}, &fakeReloader{})
	items, err := m.Listeners()
	if err != nil {
		t.Fatal(err)
	}
	l := items[0]
	// Edit reality: add a server name, drop the second short-id.
	l.Vless.Reality.ServerNames = []string{"example.com", "alt.example.com"}
	l.Vless.Reality.ShortIds = []string{"123456"}
	if err := m.UpdateListener(context.Background(), "vless-r", l); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.Path)
	text := string(data)
	for _, want := range []string{
		"show: true", "min-ver:", "client-fingerprint", "alt.example.com",
		"private-key: abcdefprivatekey", "short-id:", "network:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q after round trip:\n%s", want, text)
		}
	}
	// Re-read and verify the structured view is consistent.
	items, _ = m.Listeners()
	l2 := items[0]
	if len(l2.Vless.Reality.ServerNames) != 2 || l2.Vless.Reality.ServerNames[1] != "alt.example.com" {
		t.Fatalf("server-names not updated: %+v", l2.Vless.Reality.ServerNames)
	}
	if len(l2.Vless.Reality.ShortIds) != 1 || l2.Vless.Reality.ShortIds[0] != "123456" {
		t.Fatalf("short-ids not updated: %+v", l2.Vless.Reality.ShortIds)
	}
	if !strings.Contains(l2.Vless.Reality.ExtraYAML, "show:") {
		t.Fatalf("unknown reality field lost on update: %q", l2.Vless.Reality.ExtraYAML)
	}
}

func TestVlessRealityDeleteRemovesBlock(t *testing.T) {
	m := newManager(t, vlessRealityInitial, &fakeValidator{}, &fakeReloader{})
	items, err := m.Listeners()
	if err != nil {
		t.Fatal(err)
	}
	l := items[0]
	l.Vless.Reality = nil
	if err := m.UpdateListener(context.Background(), "vless-r", l); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.Path)
	text := string(data)
	if strings.Contains(text, "reality-config") {
		t.Fatalf("reality-config was not removed:\n%s", text)
	}
	if !strings.Contains(text, "users:") || !strings.Contains(text, "11111111-1111-1111-1111-111111111111") {
		t.Fatalf("users were lost when reality removed:\n%s", text)
	}
}

func TestVlessRealityAddFromScratch(t *testing.T) {
	initial := "mode: rule\nlisteners:\n  - name: existing\n    type: vless\n    listen: 0.0.0.0\n    port: 8443\n"
	m := newManager(t, initial, &fakeValidator{}, &fakeReloader{})
	l := Listener{
		Name: "vless-new", Type: "vless", Listen: "0.0.0.0", Port: 443,
		Vless: &Vless{
			Users: []VlessUser{{Username: "carol", UUID: "33333333-3333-3333-3333-333333333333"}},
			Reality: &Reality{
				Dest: "example.com:443", PrivateKey: "someprivatekey",
				ShortIds: []string{"aabb"}, ServerNames: []string{"example.com"}, ExtraYAML: "extra-k: v\n",
			},
		},
	}
	if err := m.CreateListener(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.Path)
	text := string(data)
	for _, want := range []string{"users:", "carol", "reality-config:", "dest: example.com:443", "short-id:", "server-names:", "extra-k: v"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q after create:\n%s", want, text)
		}
	}
}

func TestNonVlessIgnoresVless(t *testing.T) {
	initial := "mode: rule\nlisteners:\n  - name: s\n    type: mixed\n    listen: 0.0.0.0\n    port: 7890\n"
	m := newManager(t, initial, &fakeValidator{}, &fakeReloader{})
	l := Listener{Name: "s", Type: "mixed", Listen: "0.0.0.0", Port: 7890, Vless: &Vless{Users: []VlessUser{{Username: "u", UUID: "x"}}}}
	if err := m.UpdateListener(context.Background(), "s", l); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.Path)
	if strings.Contains(string(data), "users:") || strings.Contains(string(data), "uuid:") {
		t.Fatalf("non-vless listener wrote vless fields:\n%s", data)
	}
	items, _ := m.Listeners()
	if items[0].Vless != nil {
		t.Fatal("non-vless listener exposed a vless field")
	}
}

func TestNonVlessPreservesUsersInExtraYAML(t *testing.T) {
	// A non-vless listener that happens to carry users/reality-config in YAML
	// must keep them verbatim (not silently dropped), just outside the vless
	// field.
	initial := "mode: rule\nlisteners:\n  - name: s\n    type: mixed\n    listen: 0.0.0.0\n    port: 7890\n    users:\n      - username: u\n        uuid: x\n"
	m := newManager(t, initial, &fakeValidator{}, &fakeReloader{})
	items, err := m.Listeners()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Vless != nil {
		t.Fatal("non-vless listener exposed vless")
	}
	if !strings.Contains(items[0].ExtraYAML, "users:") || !strings.Contains(items[0].ExtraYAML, "username: u") {
		t.Fatalf("users not preserved in ExtraYAML: %q", items[0].ExtraYAML)
	}
	if err := m.UpdateListener(context.Background(), "s", Listener{Name: "s", Type: "mixed", Listen: "0.0.0.0", Port: 7890, ExtraYAML: items[0].ExtraYAML}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.Path)
	if !strings.Contains(string(data), "username: u") || !strings.Contains(string(data), "uuid: x") {
		t.Fatalf("users lost on update of non-vless listener:\n%s", data)
	}
}

func TestFailedReloadTransactionsPruneBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: rule\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reloader := &rollbackReloader{}
	m := &Manager{Path: path, Validator: &fakeValidator{}, Reloader: reloader}
	for i := 0; i < 8; i++ {
		if err := m.Save(context.Background(), []byte("mode: global\n")); err == nil || !strings.Contains(err.Error(), "reload failed") {
			t.Fatalf("transaction %d did not preserve reload error: %v", i, err)
		}
	}
	pattern := filepath.Join(filepath.Dir(path), filepath.Base(path)+".bak.*")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 5 {
		t.Fatalf("expected exactly 5 retained backups, got %d", len(paths))
	}
	got, _ := os.ReadFile(path)
	if string(got) != "mode: rule\n" {
		t.Fatalf("failed transactions did not leave original config: %q", got)
	}
}
