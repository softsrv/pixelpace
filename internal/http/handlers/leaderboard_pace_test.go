package handlers

import (
	"strings"
	"testing"
)

func TestFormatPace(t *testing.T) {
	tests := []struct {
		name       string
		elapsedMs  int64
		distanceMm int64
		want       string
	}{
		{
			name:       "95200 ms pace",
			elapsedMs:  95200,
			distanceMm: 500000,
			want:       "1:35.2",
		},
		{
			name:       "95500 ms pace",
			elapsedMs:  95500,
			distanceMm: 500000,
			want:       "1:35.5",
		},
		{
			name:       "101234 ms pace",
			elapsedMs:  101234,
			distanceMm: 500000,
			want:       "1:41.2",
		},
		{
			name:       "95000 ms pace",
			elapsedMs:  95000,
			distanceMm: 500000,
			want:       "1:35.0",
		},
		{
			name:       "59960 ms pace carries minute to 1:00.0",
			elapsedMs:  59960,
			distanceMm: 500000,
			want:       "1:00.0",
		},
		{
			name:       "59940 ms pace rounds to 0:59.9",
			elapsedMs:  59940,
			distanceMm: 500000,
			want:       "0:59.9",
		},
		{
			name:       "zero elapsed returns em dash",
			elapsedMs:  0,
			distanceMm: 500000,
			want:       "—",
		},
		{
			name:       "negative elapsed returns em dash",
			elapsedMs:  -1000,
			distanceMm: 500000,
			want:       "—",
		},
		{
			name:       "zero distance returns em dash",
			elapsedMs:  95000,
			distanceMm: 0,
			want:       "—",
		},
		{
			name:       "negative distance returns em dash",
			elapsedMs:  95000,
			distanceMm: -500000,
			want:       "—",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatPace(tc.elapsedMs, tc.distanceMm)
			if got != tc.want {
				t.Errorf("formatPace(%d, %d) = %q, want %q", tc.elapsedMs, tc.distanceMm, got, tc.want)
			}
			if strings.Contains(got, "NaN") || strings.Contains(got, "Inf") {
				t.Errorf("formatPace(%d, %d) produced invalid string %q (contains NaN or Inf)", tc.elapsedMs, tc.distanceMm, got)
			}
		})
	}

	// Explicitly assert that 95200 ms and 95500 ms paces differ
	pace1 := formatPace(95200, 500000)
	pace2 := formatPace(95500, 500000)
	if pace1 == pace2 {
		t.Errorf("expected formatPace(95200, 500000) and formatPace(95500, 500000) to differ, got both %q", pace1)
	}
}
