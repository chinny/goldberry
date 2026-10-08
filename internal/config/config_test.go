package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookupMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaults(t *testing.T) {
	c, err := Parse(lookupMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseURL != "sqlite:///data/goldberry.db" || c.Listen != ":8080" || c.LogFormat != "json" ||
		c.BackupRetain != 14 || c.Timezone != "UTC" || c.InsecureCookies || c.SecretKey != nil {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestFileSecretsAndProxies(t *testing.T) {
	dir := t.TempDir()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Parse(lookupMap(map[string]string{
		"GOLDBERRY_SECRET_KEY_FILE":  path,
		"GOLDBERRY_TRUSTED_PROXIES":  "10.0.0.0/8, 192.168.1.1",
		"GOLDBERRY_INSECURE_COOKIES": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SecretKey) != 32 || len(c.TrustedProxies) != 2 || !c.InsecureCookies {
		t.Fatalf("got %+v", c)
	}
}

func TestErrors(t *testing.T) {
	_, err := Parse(lookupMap(map[string]string{
		"GOLDBERRY_SECRET_KEY":    "c2hvcnQ=",
		"GOLDBERRY_LOG_FORMAT":    "xml",
		"GOLDBERRY_BACKUP_RETAIN": "0",
		"TZ":                      "Mars/Olympus",
	}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"SECRET_KEY", "LOG_FORMAT", "BACKUP_RETAIN", "TZ"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %s", err, want)
		}
	}
}

func TestEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(path, []byte("# comment\nexport GOLDBERRY_LISTEN=\":9090\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vals, err := readEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if vals["GOLDBERRY_LISTEN"] != ":9090" {
		t.Fatalf("got %v", vals)
	}
}
