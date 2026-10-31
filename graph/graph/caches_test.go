package graph

import (
	"testing"
	"time"
)

func TestParseCaches(t *testing.T) {
	c, err := ParseCaches(" versions=512, graph.selections=256 ,ttl=10m")
	if err != nil {
		t.Fatal(err)
	}
	if c.Versions.Bytes != 512<<20 || c.Selections.Bytes != 256<<20 || c.KeyIDs.Bytes != 0 {
		t.Fatalf("bytes: %+v", c)
	}
	if c.Drafts.TTL != 10*time.Minute || c.Versions.TTL != 10*time.Minute {
		t.Fatalf("ttl: %+v", c)
	}
	if c, err := ParseCaches(""); err != nil || c != (Caches{}) {
		t.Fatalf("empty: %+v %v", c, err)
	}
	for _, bad := range []string{"versions", "versions=0", "versions=a", "heads=8", "ttl=-1s", "ttl=x"} {
		if _, err := ParseCaches(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
