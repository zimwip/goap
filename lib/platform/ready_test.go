package platform

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestAwaitWithoutBus: with no bus the delays alone bring the next try.
func TestAwaitWithoutBus(t *testing.T) {
	var tries atomic.Int32
	err := (*Events)(nil).Await(context.Background(), []string{ReadyDomains}, func(context.Context) error {
		if tries.Add(1) < 2 {
			return errors.New("not yet")
		}
		return nil
	}, nil)
	if err != nil || tries.Load() != 2 {
		t.Fatalf("err %v after %d tries", err, tries.Load())
	}
}

// TestAwaitWokenByAnnouncement: an announcement brings the next try at once, long before the delay.
func TestAwaitWokenByAnnouncement(t *testing.T) {
	e := testEvents(t)
	var up atomic.Bool
	failed := make(chan struct{}, 1)
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- e.Await(context.Background(), []string{ReadyDomains}, func(context.Context) error {
			if !up.Load() {
				return errors.New("not yet")
			}
			return nil
		}, func(error, time.Duration) {
			select {
			case failed <- struct{}{}:
			default:
			}
		})
	}()
	<-failed
	up.Store(true)
	if err := e.PublishCore(ReadyDomains, map[string]string{"service": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("woken after %v, not by the announcement", d)
	}
}

// TestAwaitEndsWithContext: a context ended stops the wait.
func TestAwaitEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (*Events)(nil).Await(ctx, nil, func(context.Context) error { return errors.New("never") }, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}
