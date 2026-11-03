package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictConfiguration(t *testing.T) {
	for _, content := range []string{
		"database:\n  driver: mysql\n",
		"timezone: UTC\n",
		"worker:\n  concurrency: 12\n",
		"log:\n  level: unsupported\n",
		"http: {}\n---\nhttp: {}\n",
		"auth:\n  public_url: http://panel.example.com\n",
		"auth:\n  public_url: https://user:password@panel.example.com\n",
		"http:\n  trusted_proxies: [not-an-ip]\n",
	} {
		t.Run(strings.Split(content, "\n")[0], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestPostgresDSNEscapesSecretAndPinsTimezone(t *testing.T) {
	cfg := Defaults().Database
	cfg.Password = "p@ss:/?#% word"
	dsn, err := url.Parse(cfg.DSN("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	password, _ := dsn.User.Password()
	if password != cfg.Password {
		t.Fatal("password changed by URL encoding")
	}
	if dsn.Query().Get("TimeZone") != "Asia/Shanghai" {
		t.Fatal("missing session timezone")
	}
}

func TestPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := readPassword("", path)
	if err != nil || value != "test-secret" {
		t.Fatalf("password file failed: %v", err)
	}
	if _, err := readPassword("conflict", path); err == nil {
		t.Fatal("ambiguous password accepted")
	}
}

func TestDefaultsAndExample(t *testing.T) {
	if Defaults().Cloud.WritesEnabled {
		t.Fatal("cloud writes must default to disabled")
	}
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("../../etc/aws-panel.example.yaml"); err != nil {
		t.Fatal(err)
	}
}
func TestCloudWriteSwitchIsExplicit(t *testing.T) {
	t.Setenv("AWS_PANEL_CLOUD_WRITES_ENABLED", "true")
	cfg, err := Load("")
	if err != nil || !cfg.Cloud.WritesEnabled {
		t.Fatal("explicit cloud write switch was ignored", err)
	}
	t.Setenv("AWS_PANEL_CLOUD_WRITES_ENABLED", "invalid")
	if _, err := Load(""); err == nil {
		t.Fatal("ambiguous cloud write switch accepted")
	}
}
