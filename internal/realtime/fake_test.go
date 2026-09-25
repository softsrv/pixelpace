package realtime

import (
	"bytes"
	"context"
	"testing"
	"time"
)

const testTimeout = time.Second

func TestFakeDeliversPublishedPayload(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var broadcaster Broadcaster = NewFake()
	ch, err := broadcaster.Subscribe(ctx, "r1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	payload := []byte("hello")
	published := make(chan error, 1)
	go func() { published <- broadcaster.Publish(ctx, "r1", payload) }()
	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Publish did not return before deadline")
	}
	select {
	case got, ok := <-ch:
		if !ok || !bytes.Equal(got, payload) {
			t.Fatalf("received (%q, %v), want (%q, true)", got, ok, payload)
		}
	case <-ctx.Done():
		t.Fatal("payload not received before deadline")
	}
}

func TestFakeContextCancelClosesChannel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := NewFake()
	ch, err := fake.Subscribe(ctx, "r1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	cancel()
	cancel() // Repeated cancellation must not double-close the channel.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("subscription channel is still open")
		}
	case <-time.After(testTimeout):
		t.Fatal("subscription channel did not close before deadline")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.subscribers) != 0 {
		t.Fatal("cancelled subscriber remains registered")
	}
}

func TestFakePublishNonBlockingDropsWhenFull(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	fake := NewFake()
	ch, err := fake.Subscribe(ctx, "r1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if cap(ch) != subBufferSize {
		t.Fatalf("buffer capacity = %d, want %d", cap(ch), subBufferSize)
	}
	published := make(chan error, 1)
	go func() {
		for i := 0; i < subBufferSize+50; i++ {
			if err := fake.Publish(ctx, "r1", []byte("hello")); err != nil {
				published <- err
				return
			}
		}
		published <- nil
	}()
	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("publisher blocked on a full buffer")
	}
	for count := 0; ; {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("subscription closed before draining completed")
			}
			count++
			if count > subBufferSize {
				t.Fatalf("received more than %d buffered messages", subBufferSize)
			}
		case <-ctx.Done():
			t.Fatal("draining did not finish before deadline")
		default:
			if count != subBufferSize {
				t.Fatalf("received %d messages, want %d", count, subBufferSize)
			}
			return
		}
	}
}

func TestFakeConcurrentPublishAndCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	fake := NewFake()
	published := make(chan struct{})
	go func() {
		defer close(published)
		for ctx.Err() == nil {
			_ = fake.Publish(ctx, "r1", []byte("hello"))
		}
	}()
	for i := 0; i < 100; i++ {
		subCtx, unsubscribe := context.WithCancel(ctx)
		ch, err := fake.Subscribe(subCtx, "r1")
		unsubscribe()
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	closed:
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					break closed
				}
			case <-ctx.Done():
				t.Fatal("concurrent cancellation did not close channel before deadline")
			}
		}
	}
	cancel()
	select {
	case <-published:
	case <-time.After(testTimeout):
		t.Fatal("concurrent publisher did not stop before deadline")
	}
}
