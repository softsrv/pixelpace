package main

import "testing"

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
