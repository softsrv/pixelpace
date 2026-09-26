package realtime

import "context"

// Broadcaster abstracts real-time pub/sub for a telemetry stream.
// Payloads are opaque bytes: the interface abstracts transport, not the domain.
type Broadcaster interface {
	Publish(ctx context.Context, raceID string, payload []byte) error
	Subscribe(ctx context.Context, raceID string) (<-chan []byte, error)
}
