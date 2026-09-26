package realtime

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// ValkeyBroadcaster delivers opaque payloads through Valkey pub/sub.
// The caller owns the client and must keep it open while subscriptions are active.
type ValkeyBroadcaster struct {
	client *redis.Client
}

var _ Broadcaster = (*ValkeyBroadcaster)(nil)

// NewValkeyBroadcaster creates a broadcaster using client.
func NewValkeyBroadcaster(client *redis.Client) *ValkeyBroadcaster {
	return &ValkeyBroadcaster{client: client}
}

// Publish delivers payload to the Valkey channel for raceID.
func (b *ValkeyBroadcaster) Publish(ctx context.Context, raceID string, payload []byte) error {
	return b.client.Publish(ctx, channelName(raceID), payload).Err()
}

// Subscribe registers a subscription until ctx is cancelled.
// Callers must cancel ctx when they no longer need the subscription.
// Delivery is best-effort; the subscription may not be established on return.
func (b *ValkeyBroadcaster) Subscribe(ctx context.Context, raceID string) (<-chan []byte, error) {
	pubsub := b.client.Subscribe(ctx, channelName(raceID))
	out := make(chan []byte)

	go func() {
		defer close(out)
		defer pubsub.Close()

		messages := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}
				select {
				case out <- []byte(msg.Payload):
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

func channelName(raceID string) string {
	return "race:" + raceID
}
