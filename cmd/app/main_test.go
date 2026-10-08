package main

import "testing"

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
