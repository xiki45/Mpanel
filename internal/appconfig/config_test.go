package appconfig

import (
	"strings"
	"testing"
)

func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("MPANEL_PASSWORD", "")
	t.Setenv("MPANEL_SESSION_SECRET", "short")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MPANEL_PASSWORD") || !strings.Contains(err.Error(), "MPANEL_SESSION_SECRET") {
		t.Fatalf("expected clear required configuration error, got %v", err)
	}
}

func TestLoadRejectsInvalidAPIURL(t *testing.T) {
	t.Setenv("MPANEL_PASSWORD", "password")
	t.Setenv("MPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("MIHOMO_API_URL", "file:///etc/passwd")
	if _, err := Load(); err == nil {
		t.Fatal("non-HTTP API URL accepted")
	}
}
