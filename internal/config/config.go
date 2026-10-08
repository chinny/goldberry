// Package config loads Goldberry's configuration from environment variables
// (plan §12.2). An optional /data/config.env file supplies defaults; real
// environment variables always win. Any variable may instead be given as
// NAME_FILE pointing at a file that holds the value (Docker and K8s secrets).
package config

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the parsed runtime configuration.
type Config struct {
	DatabaseURL     string
	SecretKey       []byte // nil when unset; Phase 3 uses it to encrypt SMTP secrets
	BaseURL         string
	Listen          string
	TrustedProxies  []netip.Prefix
	InsecureCookies bool
	BackupSchedule  string
	BackupRetain    int
	LogFormat       string // json | text
	Timezone        string // default household timezone at setup
	SMTP            SMTP
}

// SMTP holds env-var overrides for email settings. Empty fields are unset.
type SMTP struct {
	Host, Port, Security, Username, Password, From string
}

// DefaultEnvFile is read when present, before the process environment.
const DefaultEnvFile = "/data/config.env"

// Load reads configuration using os.LookupEnv, with envFile as defaults.
func Load(envFile string) (Config, error) {
	fileVals, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	return parse(func(k string) (string, bool) {
		if v, ok := os.LookupEnv(k); ok {
			return v, true
		}
		v, ok := fileVals[k]
		return v, ok
	})
}

// Parse builds a Config from an arbitrary lookup function (used by tests).
func Parse(lookup func(string) (string, bool)) (Config, error) { return parse(lookup) }

func parse(lookup func(string) (string, bool)) (Config, error) {
	get := func(name, def string) (string, error) {
		if v, ok := lookup(name); ok && v != "" {
			return v, nil
		}
		if path, ok := lookup(name + "_FILE"); ok && path != "" {
			b, err := os.ReadFile(path) //nolint:gosec // the operator names this file (NAME_FILE)
			if err != nil {
				return "", fmt.Errorf("%s_FILE: %w", name, err)
			}
			return strings.TrimSpace(string(b)), nil
		}
		return def, nil
	}

	var errs []error
	str := func(name, def string) string {
		v, err := get(name, def)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}

	c := Config{
		DatabaseURL:    str("GOLDBERRY_DATABASE_URL", "sqlite:///data/goldberry.db"),
		BaseURL:        strings.TrimRight(str("GOLDBERRY_BASE_URL", ""), "/"),
		Listen:         str("GOLDBERRY_LISTEN", ":8080"),
		BackupSchedule: str("GOLDBERRY_BACKUP_SCHEDULE", "03:15"),
		LogFormat:      str("GOLDBERRY_LOG_FORMAT", "json"),
		Timezone:       str("TZ", "UTC"),
		SMTP: SMTP{
			Host:     str("GOLDBERRY_SMTP_HOST", ""),
			Port:     str("GOLDBERRY_SMTP_PORT", ""),
			Security: str("GOLDBERRY_SMTP_SECURITY", ""),
			Username: str("GOLDBERRY_SMTP_USERNAME", ""),
			Password: str("GOLDBERRY_SMTP_PASSWORD", ""),
			From:     str("GOLDBERRY_SMTP_FROM", ""),
		},
	}

	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("GOLDBERRY_LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		errs = append(errs, fmt.Errorf("TZ: %w", err))
	}
	if _, err := ParseClock(c.BackupSchedule); err != nil {
		errs = append(errs, fmt.Errorf("GOLDBERRY_BACKUP_SCHEDULE: %w", err))
	}

	retain := str("GOLDBERRY_BACKUP_RETAIN", "14")
	if n, err := strconv.Atoi(retain); err != nil || n < 1 {
		errs = append(errs, fmt.Errorf("GOLDBERRY_BACKUP_RETAIN must be a positive integer, got %q", retain))
	} else {
		c.BackupRetain = n
	}

	insecure := str("GOLDBERRY_INSECURE_COOKIES", "false")
	if b, err := strconv.ParseBool(insecure); err != nil {
		errs = append(errs, fmt.Errorf("GOLDBERRY_INSECURE_COOKIES: %w", err))
	} else {
		c.InsecureCookies = b
	}

	for _, s := range strings.FieldsFunc(str("GOLDBERRY_TRUSTED_PROXIES", ""), func(r rune) bool { return r == ',' || r == ' ' }) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			if a, aerr := netip.ParseAddr(s); aerr == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				errs = append(errs, fmt.Errorf("GOLDBERRY_TRUSTED_PROXIES: %w", err))
				continue
			}
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}

	if key := str("GOLDBERRY_SECRET_KEY", ""); key != "" {
		b, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			b, err = base64.RawStdEncoding.DecodeString(key)
		}
		switch {
		case err != nil:
			errs = append(errs, errors.New("GOLDBERRY_SECRET_KEY must be base64 (try: openssl rand -base64 32)"))
		case len(b) < 32:
			errs = append(errs, fmt.Errorf("GOLDBERRY_SECRET_KEY must decode to at least 32 bytes, got %d", len(b)))
		default:
			c.SecretKey = b
		}
	}

	return c, errors.Join(errs...)
}

// ParseClock parses "HH:MM" into hours and minutes since midnight.
func ParseClock(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("want HH:MM, got %q", s)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

func readEnvFile(path string) (map[string]string, error) {
	vals := map[string]string{}
	if path == "" {
		return vals, nil
	}
	f, err := os.Open(path) //nolint:gosec // fixed config path
	if errors.Is(err, os.ErrNotExist) {
		return vals, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		vals[strings.TrimSpace(k)] = v
	}
	return vals, sc.Err()
}
