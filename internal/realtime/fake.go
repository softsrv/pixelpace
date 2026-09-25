package realtime

import (
	"context"
	"sync"
)

const subBufferSize = 16

type subscriber struct {
	delivery chan []byte
	done     <-chan struct{}
}

// Fake is an in-memory Broadcaster with bounded, best-effort delivery.
// Payloads are shared with subscribers and must not be modified after publishing.
// The zero value is ready to use.
type Fake struct {
	mu          sync.Mutex
	subscribers map[string]map[*subscriber]struct{}
}

var _ Broadcaster = (*Fake)(nil)

// NewFake creates an in-memory broadcaster.
func NewFake() *Fake {
	return &Fake{}
}

// Subscribe registers a buffered subscription until ctx is cancelled.
// Callers must cancel ctx when they no longer need the subscription.
func (f *Fake) Subscribe(ctx context.Context, raceID string) (<-chan []byte, error) {
	sub := &subscriber{
		delivery: make(chan []byte, subBufferSize),
		done:     ctx.Done(),
	}

	f.mu.Lock()
	if f.subscribers == nil {
		f.subscribers = make(map[string]map[*subscriber]struct{})
	}
	if f.subscribers[raceID] == nil {
		f.subscribers[raceID] = make(map[*subscriber]struct{})
	}
	f.subscribers[raceID][sub] = struct{}{}
	f.mu.Unlock()

	go func() {
		<-sub.done
		f.mu.Lock()
		defer f.mu.Unlock()

		subs := f.subscribers[raceID]
		if _, ok := subs[sub]; ok {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(f.subscribers, raceID)
			}
			// Publish holds the same lock while sending, so close cannot race it.
			close(sub.delivery)
		}
	}()

	return sub.delivery, nil
}

// Publish delivers payload to each subscriber of raceID, dropping it if full.
func (f *Fake) Publish(_ context.Context, raceID string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for sub := range f.subscribers[raceID] {
		select {
		case sub.delivery <- payload:
		default:
			// Slow subscribers must not block the publisher.
		}
	}
	return nil
}
