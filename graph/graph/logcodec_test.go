package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// memDicts is a logDictStore in memory.
type memDicts struct {
	mu      sync.Mutex
	stored  []logDict
	entries map[string][][]byte
}

func (m *memDicts) dicts(_ context.Context, after uint32) ([]logDict, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []logDict
	for _, d := range m.stored {
		if d.ID > after {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memDicts) samples(_ context.Context, typ string, n int) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entries[typ]
	return s[max(0, len(s)-n):], nil
}

func (m *memDicts) addDict(_ context.Context, typ string, build func(id uint32) ([]byte, error)) (logDict, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uint32(len(m.stored) + 1)
	d, err := build(id)
	if err != nil {
		return logDict{}, err
	}
	m.stored = append(m.stored, logDict{ID: id, Type: typ, Dict: d})
	return m.stored[len(m.stored)-1], nil
}

func sameJSON(t *testing.T, want, got []byte) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(want, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &b); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	wa, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if !bytes.Equal(wa, gb) {
		t.Fatalf("payload changed:\nwant %s\ngot  %s", want, got)
	}
}

func TestLogCodecRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newLogCodec(nil)
	for _, j := range []string{
		`null`, `{}`, `[]`, `"x"`, `0`, `-9`, `1.5`, `true`,
		`{"impact":"0193b1c2-7d4e-7a1b-8c2d-3e4f5a6b7c8d","node":"0193B1C2-7D4E-7A1B-8C2D-3E4F5A6B7C8D","n":18446744073709551615,"big":123456789012345678901234567890,"f":1e300,"neg":-42}`,
		`{"html":"<a href=\"x\">&amp;</a>","u":"é ✓","nested":[{"a":[1,2,{"b":null}]}]}`,
		`{"notuuid":"0193b1c2-7d4e-7a1b-8c2d-3e4f5a6b7c8","dashes":"0193b1c2x7d4e-7a1b-8c2d-3e4f5a6b7c8d"}`,
	} {
		p, err := c.encode(ctx, nil, "impact.updated", []byte(j))
		if err != nil {
			t.Fatalf("%s: %v", j, err)
		}
		if p[0] != payloadCBOR && p[0] != payloadZstd {
			t.Fatalf("%s: stored as %x", j, p)
		}
		got, err := c.decode(ctx, nil, p)
		if err != nil {
			t.Fatalf("%s: %v", j, err)
		}
		sameJSON(t, []byte(j), got)
	}
	// the canonical form of a UUID is kept as 16 bytes, any other as text
	raw, _ := jsonToCBOR([]byte(`"0193b1c2-7d4e-7a1b-8c2d-3e4f5a6b7c8d"`))
	if len(raw) != 1+1+1+16 { // tag 37, a byte string of 16
		t.Fatalf("uuid encoded in %d bytes: %x", len(raw), raw)
	}
	got, _ := c.decode(ctx, nil, append([]byte{payloadCBOR}, raw...))
	if string(got) != `"0193b1c2-7d4e-7a1b-8c2d-3e4f5a6b7c8d"` {
		t.Fatalf("uuid back as %s", got)
	}
	// an entry written as JSON, before ADR 0180 §2, reads as is; an empty one as none
	if got, _ := c.decode(ctx, nil, []byte(`{"a": 1}`)); string(got) != `{"a": 1}` {
		t.Fatalf("JSON entry read as %s", got)
	}
	if p, _ := c.encode(ctx, nil, "x", nil); len(p) != 0 {
		t.Fatalf("an empty payload stored as %x", p)
	}
	if got, _ := c.decode(ctx, nil, nil); got != nil {
		t.Fatalf("an empty payload read as %s", got)
	}
	// a key order or a spelling of the same JSON is the same stored payload (deterministic CBOR)
	a, _ := c.encode(ctx, nil, "x", []byte(`{"b":1,"a":[true,"x"]}`))
	b, _ := c.encode(ctx, nil, "x", []byte(`{ "a": [true, "x"], "b": 1 }`))
	if !bytes.Equal(a, b) {
		t.Fatalf("not deterministic: %x / %x", a, b)
	}
}

// entry is a payload like the impact entries of a change.
func entry(i int) []byte {
	return fmt.Appendf(nil, `{"op":"updated","patch":{"properties":{"title":{"from":"Requirement %d","to":"Requirement %d (rev)"},"priority":{"from":"low","to":"high"}}},"impact":"0193b1c2-7d4e-7a1b-8c2d-%012x"}`, i, i, i)
}

func TestLogCodecDictionaries(t *testing.T) {
	ctx := context.Background()
	saved := logTrainAt
	logTrainAt = []int{50, 200}
	defer func() { logTrainAt = saved }()

	store := &memDicts{entries: map[string][][]byte{}}
	c := newLogCodec(store)
	write := func(from, to int) (size int) {
		for i := from; i < to; i++ {
			p, err := c.encode(ctx, nil, "impact.updated", entry(i))
			if err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			store.entries["impact.updated"] = append(store.entries["impact.updated"], p)
			store.mu.Unlock()
			size += len(p)
		}
		c.wait()
		return size
	}
	before := write(0, 50)
	if len(store.stored) != 1 {
		t.Fatalf("%d dictionaries after 50 entries, want 1", len(store.stored))
	}
	after := write(50, 100)
	if after >= before {
		t.Fatalf("the dictionary does not help: %d bytes, %d without", after, before)
	}
	write(100, 300)
	if len(store.stored) != 2 {
		t.Fatalf("%d dictionaries after 250 entries since the first, want 2", len(store.stored))
	}
	write(300, 1000)
	if len(store.stored) != 2 {
		t.Fatalf("%d dictionaries: none after the last threshold", len(store.stored))
	}

	// another process reads every entry, learning the dictionaries as it meets them
	other := newLogCodec(store)
	for i, p := range store.entries["impact.updated"] {
		got, err := other.decode(ctx, nil, p)
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		sameJSON(t, entry(i), got)
	}
}
