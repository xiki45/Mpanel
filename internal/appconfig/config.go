package appconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr       string
	Username         string
	Password         string
	SessionSecret    []byte
	MihomoAPIURL     *url.URL
	MihomoAPISecret  string
	MihomoConfigPath string
	MihomoBinary     string
	CommandTimeout   time.Duration
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:       value("MPANEL_LISTEN_ADDR", "127.0.0.1:8080"),
		Username:         value("MPANEL_USERNAME", "admin"),
		Password:         os.Getenv("MPANEL_PASSWORD"),
		SessionSecret:    []byte(os.Getenv("MPANEL_SESSION_SECRET")),
		MihomoAPISecret:  os.Getenv("MIHOMO_API_SECRET"),
		MihomoConfigPath: value("MIHOMO_CONFIG_PATH", "/etc/mihomo/config.yaml"),
		MihomoBinary:     value("MIHOMO_BINARY", "/usr/local/bin/mihomo"),
		CommandTimeout:   15 * time.Second,
	}
	var missing []string
	if c.Password == "" {
		missing = append(missing, "MPANEL_PASSWORD")
	}
	if len(c.SessionSecret) < 32 {
		missing = append(missing, "MPANEL_SESSION_SECRET (at least 32 bytes)")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing or invalid required configuration: %s", strings.Join(missing, ", "))
	}
	rawURL := value("MIHOMO_API_URL", "http://127.0.0.1:9090")
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return Config{}, errors.New("MIHOMO_API_URL must be an absolute HTTP(S) URL without credentials")
	}
	c.MihomoAPIURL = u
	return c, nil
}

func value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
