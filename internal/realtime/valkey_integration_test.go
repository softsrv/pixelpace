//go:build integration

package realtime

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// testValkey connects to VALKEY_ADDR, skipping when Valkey is unavailable.
// Run with: VALKEY_ADDR=localhost:6379 go test -tags integration ./internal/realtime
func testValkey(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		t.Skip("VALKEY_ADDR not set; skipping integration test")
	}
	client := redis.NewClient(&redis.Options{
		Addr:                  addr,
		DialTimeout:           500 * time.Millisecond,
		ReadTimeout:           500 * time.Millisecond,
		WriteTimeout:          500 * time.Millisecond,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("Valkey unavailable; skipping integration test: %v", err)
	}
	return client
}

// waitValkeySubscribers observes server-side registration and teardown rather
// than guessing when the asynchronous subscription has reached Valkey.
func waitValkeySubscribers(t *testing.T, client *redis.Client, raceID string, want int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	channel := "race:" + raceID
	for {
		counts, err := client.PubSubNumSub(ctx, channel).Result()
		if err != nil {
			t.Fatalf("PubSubNumSub: %v", err)
		}
		if counts[channel] == want {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("subscriber count = %d, want %d", counts[channel], want)
		}
	}
}

func TestIntegration_ValkeyRoundTrip(t *testing.T) {
	client := testValkey(t)
	broadcaster := NewValkeyBroadcaster(client)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raceID := uuid.Must(uuid.NewV7()).String()
	ch, err := broadcaster.Subscribe(ctx, raceID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	waitValkeySubscribers(t, client, raceID, 1)
	payload := []byte{0, 1, 127, 128, 255, 'h', 'i', '\n'}
	if err := broadcaster.Publish(ctx, raceID, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case got, ok := <-ch:
		if !ok || !bytes.Equal(got, payload) {
			t.Fatalf("received (%q, %v), want (%q, true)", got, ok, payload)
		}
	case <-time.After(time.Second):
		t.Fatal("payload not received before deadline")
	}
}

func TestIntegration_ValkeyCancelClosesSubscription(t *testing.T) {
	for _, slowConsumer := range []bool{false, true} {
		name := "idle"
		if slowConsumer {
			name = "unread_payload"
		}
		t.Run(name, func(t *testing.T) {
			client := testValkey(t)
			broadcaster := NewValkeyBroadcaster(client)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raceID := uuid.Must(uuid.NewV7()).String()
			ch, err := broadcaster.Subscribe(ctx, raceID)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			waitValkeySubscribers(t, client, raceID, 1)
			if slowConsumer {
				if err := broadcaster.Publish(ctx, raceID, []byte("unread")); err != nil {
					t.Fatalf("Publish: %v", err)
				}
				// Leave the consumer idle while delivery reaches the unbuffered channel.
				time.Sleep(50 * time.Millisecond)
			}
			cancel()
			// Check teardown before reading: cancellation cannot depend on a consumer.
			waitValkeySubscribers(t, client, raceID, 0)
			select {
			case _, ok := <-ch:
				if ok {
					t.Fatal("subscription channel is still open")
				}
			case <-time.After(time.Second):
				t.Fatal("subscription channel did not close before deadline")
			}
		})
	}
}
