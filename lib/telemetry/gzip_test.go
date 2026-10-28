package telemetry

import (
	"bytes"
	"compress/gzip"
	"io"
	"runtime"
	"testing"
	"time"
)

// A gzip writer Connect lets go of comes back to the free list, not to the collector, and a new writer takes it.
func TestGzipWritersOutliveACollection(t *testing.T) {
	for len(gzipFree) > 0 {
		<-gzipFree
	}
	func() {
		c := newGzipWriter()
		var out bytes.Buffer
		c.Reset(&out)
		if _, err := c.Write([]byte("answer")); err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		r, err := gzip.NewReader(&out)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := io.ReadAll(r); string(b) != "answer" {
			t.Fatalf("read back %q", b)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(gzipFree) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the writer did not come back to the free list")
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	// the free list may hold writers of earlier tests too: the new writer is one of them
	free := map[*gzip.Writer]bool{}
	for len(gzipFree) > 0 {
		free[<-gzipFree] = true
	}
	for w := range free {
		gzipFree <- w
	}
	if got := newGzipWriter().(*recycled).Writer; !free[got] {
		t.Fatal("a new writer did not take one recycled")
	}
}
