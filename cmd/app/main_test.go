package main

import (
	"testing"
	"time"
)

func TestConfigValkeyAddr(t *testing.T) {
	for key, value := range map[string]string{
		"DATABASE_URL": "postgres://localhost/test",
		"APP_BASE_URL": "http://localhost:8080",
		"JWT_SECRET":   "test-secret-at-least-thirty-two-bytes",
		"SMTP_HOST":    "localhost", "SMTP_PORT": "2525",
		"SMTP_FROM_EMAIL": "test@example.com",
	} {
		t.Setenv(key, value)
	}
	for _, tc := range []struct{ name, addr, want string }{
		{"default", "", "localhost:6379"},
		{"configured", "valkey:6380", "valkey:6380"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VALKEY_ADDR", tc.addr)
			if got := mustLoadConfig().ValkeyAddr; got != tc.want {
				t.Fatalf("ValkeyAddr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDevModeForAppEnv(t *testing.T) {
	tests := []struct {
		name   string
		appEnv string
		want   bool
	}{
		{name: "development", appEnv: "development", want: true},
		{name: "staging", appEnv: "staging", want: true},
		{name: "production", appEnv: "production", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := devModeForAppEnv(tt.appEnv); got != tt.want {
				t.Fatalf("devModeForAppEnv(%q) = %v, want %v", tt.appEnv, got, tt.want)
			}
		})
	}
}

func TestConcept2Config(t *testing.T) {
	baseEnv := map[string]string{
		"DATABASE_URL":    "postgres://localhost/test",
		"APP_BASE_URL":    "http://localhost:8080",
		"JWT_SECRET":      "test-secret-at-least-thirty-two-bytes",
		"SMTP_HOST":       "localhost",
		"SMTP_PORT":       "2525",
		"SMTP_FROM_EMAIL": "test@example.com",
	}

	setBaseEnv := func(t *testing.T) {
		t.Helper()
		for k, v := range baseEnv {
			t.Setenv(k, v)
		}
	}

	t.Run("both unset -> disabled, default base URL and derived redirect", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("CONCEPT2_CLIENT_ID", "")
		t.Setenv("CONCEPT2_CLIENT_SECRET", "")
		t.Setenv("CONCEPT2_BASE_URL", "")

		cfg := mustLoadConfig()
		if cfg.Concept2Enabled() {
			t.Fatal("expected Concept2 to be disabled when credentials are unset")
		}
		if got, want := cfg.Concept2BaseURL, "https://log-dev.concept2.com"; got != want {
			t.Fatalf("Concept2BaseURL = %q, want %q", got, want)
		}
		if got, want := cfg.Concept2RedirectURL(), "http://localhost:8080/profile/concept2/callback"; got != want {
			t.Fatalf("Concept2RedirectURL = %q, want %q", got, want)
		}
	})

	t.Run("only client id set -> disabled", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("CONCEPT2_CLIENT_ID", "client-123")
		t.Setenv("CONCEPT2_CLIENT_SECRET", "")

		cfg := mustLoadConfig()
		if cfg.Concept2Enabled() {
			t.Fatal("expected Concept2 to be disabled when only client id is set")
		}
	})

	t.Run("only client secret set -> disabled", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("CONCEPT2_CLIENT_ID", "")
		t.Setenv("CONCEPT2_CLIENT_SECRET", "secret-456")

		cfg := mustLoadConfig()
		if cfg.Concept2Enabled() {
			t.Fatal("expected Concept2 to be disabled when only client secret is set")
		}
	})

	t.Run("both credentials set -> enabled, default base URL and redirect derived from APP_BASE_URL", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("APP_BASE_URL", "https://pixelpace.example.com")
		t.Setenv("CONCEPT2_CLIENT_ID", "client-123")
		t.Setenv("CONCEPT2_CLIENT_SECRET", "secret-456")
		t.Setenv("CONCEPT2_BASE_URL", "")

		cfg := mustLoadConfig()
		if !cfg.Concept2Enabled() {
			t.Fatal("expected Concept2 to be enabled when both credentials are set")
		}
		if got, want := cfg.Concept2BaseURL, "https://log-dev.concept2.com"; got != want {
			t.Fatalf("Concept2BaseURL = %q, want %q", got, want)
		}
		if got, want := cfg.Concept2RedirectURL(), "https://pixelpace.example.com/profile/concept2/callback"; got != want {
			t.Fatalf("Concept2RedirectURL = %q, want %q", got, want)
		}
	})

	t.Run("custom base URL", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("CONCEPT2_CLIENT_ID", "client-123")
		t.Setenv("CONCEPT2_CLIENT_SECRET", "secret-456")
		t.Setenv("CONCEPT2_BASE_URL", "https://log.concept2.com")

		cfg := mustLoadConfig()
		if got, want := cfg.Concept2BaseURL, "https://log.concept2.com"; got != want {
			t.Fatalf("Concept2BaseURL = %q, want %q", got, want)
		}
	})
}

// CLM-15: the binary embeds the timezone database (time/tzdata), so
// time.LoadLocation works with no zoneinfo on the host.
func TestTimezoneDataEmbedded(t *testing.T) {
	t.Setenv("ZONEINFO", "")
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation(America/New_York): %v", err)
	}
	if loc.String() != "America/New_York" {
		t.Fatalf("location = %q, want America/New_York", loc.String())
	}
}
