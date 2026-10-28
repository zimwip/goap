package typecat

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zimwip/goap/lib/def"
)

// A type the catalogue misses reads the source again, once per interval, and says whether the domains changed.
func TestLiveRefresh(t *testing.T) {
	v1 := parse(t, "name: shop\nversion: 1.0.0\nnodeTypes:\n  - name: Order\n")
	v2 := parse(t, "name: shop\nversion: 1.1.0\nnodeTypes:\n  - name: Order\n  - name: Invoice\n")
	var reads atomic.Int32
	cur := atomic.Pointer[def.Domain]{}
	cur.Store(v1)
	fail := atomic.Bool{}
	l := NewLive(func(context.Context) ([]*def.Domain, error) {
		reads.Add(1)
		if fail.Load() {
			return nil, errors.New("registry down")
		}
		return []*def.Domain{cur.Load()}, nil
	})
	ctx := context.Background()
	if err := l.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.Get().CheckNode("shop", "shop@Invoice"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("not published yet: %v", err)
	}
	if l.Refresh(ctx, time.Hour) {
		t.Fatal("nothing new was published")
	}
	cur.Store(v2)
	if l.Refresh(ctx, time.Hour) {
		t.Fatal("read again within the interval")
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("one read for the reload, one for the first miss: %d", n)
	}
	if !l.Refresh(ctx, 0) {
		t.Fatal("the new version is in force")
	}
	if err := l.Get().CheckNode("shop", "shop@Invoice"); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if l.Refresh(ctx, 0) {
		t.Fatal("a failed read changes nothing")
	}
	if _, ok := l.Get().Type("shop@Invoice"); !ok {
		t.Fatal("the catalogue kept stays in force")
	}
}
