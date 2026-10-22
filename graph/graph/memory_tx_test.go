package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/platform"
)

// A memory transaction copies the state only at its first write: a read copies nothing, a failed write is undone.
func TestMemoryTxSnapshotsAtFirstWrite(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	boom := errors.New("boom")
	_ = m.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Changes(ctx); err != nil {
			t.Fatal(err)
		}
		if tx.(*memTx).snap != nil {
			t.Fatal("a read took a snapshot")
		}
		return boom
	})
	err := m.InTx(ctx, func(tx Tx) error {
		if err := tx.PutChange(ctx, domain.Change{ID: "C-1", Title: "undone"}); err != nil {
			t.Fatal(err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	_ = m.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Change(ctx, "C-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the failed write stayed: %v", err)
		}
		return nil
	})
}

// A read-only context reads beside the other readers; a write in it runs the transaction again on the writer.
func TestMemoryReadOnlyTxWritesOnTheWriter(t *testing.T) {
	ctx := platform.ReadOnly(context.Background())
	m := NewMemory()
	runs := 0
	err := m.InTx(ctx, func(tx Tx) error {
		runs++
		return tx.PutChange(ctx, domain.Change{ID: "C-1", Title: "written"})
	})
	if err != nil || runs != 2 {
		t.Fatalf("runs %d: %v", runs, err)
	}
	if err := m.InTx(ctx, func(tx Tx) error {
		if tx.(*memTx).snap != nil || !tx.(*memTx).readOnly {
			t.Fatal("a read-only read is not on the readers")
		}
		_, err := tx.Change(ctx, "C-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
