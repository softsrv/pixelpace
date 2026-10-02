package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/softsrv/starter/internal/app"
)

type raceBeginProbe struct {
	t     *testing.T
	slept *bool
	err   error
}

func (p raceBeginProbe) Begin(context.Context) (pgx.Tx, error) {
	p.t.Helper()
	if !*p.slept {
		p.t.Fatal("database transaction began before countdown completed")
	}
	return nil, p.err
}

func TestRaceServiceCountdown(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configured, want time.Duration
	}{
		{"default", 0, 10 * time.Second},
		{"configured", time.Millisecond, time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slept := false
			stop := errors.New("stop at transaction boundary")
			svc := app.NewRaceService(nil, raceBeginProbe{t, &slept, stop}, nil, app.RaceServiceConfig{
				CountdownDuration: tc.configured,
				Sleep: func(_ context.Context, duration time.Duration) error {
					if duration != tc.want {
						t.Fatalf("countdown = %v, want %v", duration, tc.want)
					}
					slept = true
					return nil
				},
			})
			_, err := svc.Begin(context.Background(), uuid.Must(uuid.NewV7()))
			if !slept || !errors.Is(err, stop) {
				t.Fatalf("Begin: slept=%v, error=%v", slept, err)
			}
		})
	}
}

func TestRaceServiceCountdownWaits(t *testing.T) {
	const duration = 5 * time.Millisecond
	start := time.Now()
	slept := true // This test observes the real timer instead of injecting Sleep.
	stop := errors.New("stop at transaction boundary")
	svc := app.NewRaceService(nil, raceBeginProbe{t, &slept, stop}, nil, app.RaceServiceConfig{CountdownDuration: duration})
	if _, err := svc.Begin(context.Background(), uuid.Must(uuid.NewV7())); !errors.Is(err, stop) {
		t.Fatalf("Begin: %v", err)
	}
	if elapsed := time.Since(start); elapsed < duration {
		t.Fatalf("Begin waited %v, want at least %v", elapsed, duration)
	}
}

func TestRaceServiceCountdownCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := app.NewRaceService(nil, nil, nil, app.RaceServiceConfig{})
	if _, err := svc.Begin(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, context.Canceled) {
		t.Fatalf("Begin canceled countdown: %v", err)
	}
}
