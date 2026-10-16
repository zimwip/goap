package platform

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// events returns a live Events connection, or skips the test when
// GOAP_TEST_NATS_URL is not set (mirrors GOAP_TEST_PG_DSN, internal/pgtest).
func testEvents(t *testing.T) *Events {
	t.Helper()
	url := os.Getenv("GOAP_TEST_NATS_URL")
	if url == "" {
		t.Skip("GOAP_TEST_NATS_URL not set")
	}
	ctx := context.Background()
	e, err := ConnectEvents(ctx, slog.New(slog.DiscardHandler), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestLeaseAcquireAndSteal(t *testing.T) {
	ctx := context.Background()
	e := testEvents(t)
	bucket := "test-leases"
	lease, err := e.NewLease(ctx, bucket)
	if err != nil {
		t.Fatal(err)
	}
	const key = "trigger-manager"
	const ttl = 200 * time.Millisecond

	held, err := lease.Acquire(ctx, key, "a", ttl)
	if err != nil || !held {
		t.Fatalf("a should acquire the free lease: held=%v err=%v", held, err)
	}
	held, err = lease.Acquire(ctx, key, "b", ttl)
	if err != nil || held {
		t.Fatalf("b must not acquire a's fresh lease: held=%v err=%v", held, err)
	}
	held, err = lease.Acquire(ctx, key, "a", ttl)
	if err != nil || !held {
		t.Fatalf("a should renew its own lease: held=%v err=%v", held, err)
	}

	time.Sleep(2 * ttl)
	held, err = lease.Acquire(ctx, key, "b", ttl)
	if err != nil || !held {
		t.Fatalf("b should steal a's expired lease: held=%v err=%v", held, err)
	}
	held, err = lease.Acquire(ctx, key, "a", ttl)
	if err != nil || held {
		t.Fatalf("a must not reacquire after b stole the lease: held=%v err=%v", held, err)
	}
}
