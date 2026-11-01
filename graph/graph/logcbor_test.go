package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// randomValue is a JSON value of any shape.
func randomValue(r *rand.Rand, depth int) any {
	switch k := r.IntN(10); {
	case depth > 4 || k < 2:
		return randomString(r)
	case k == 2:
		return r.Int64N(1<<62) - 1<<61
	case k == 3:
		return r.NormFloat64() * math.Pow(10, float64(r.IntN(40)-20))
	case k == 4:
		return r.IntN(2) == 0
	case k == 5:
		return nil
	case k == 6:
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", r.Uint32(), r.Uint32()&0xffff, r.Uint32()&0xffff, r.Uint32()&0xffff, r.Uint64()&0xffffffffffff)
	case k == 7:
		a := make([]any, r.IntN(8))
		for i := range a {
			a[i] = randomValue(r, depth+1)
		}
		return a
	default:
		m := map[string]any{}
		for range r.IntN(8) {
			m[randomString(r)] = randomValue(r, depth+1)
		}
		return m
	}
}

func randomString(r *rand.Rand) string {
	const alphabet = "abcXYZ019 \"\\/<>&\n\t\x01é✓\u2028😀"
	runes := []rune(alphabet)
	var b strings.Builder
	for range r.IntN(40) {
		b.WriteRune(runes[r.IntN(len(runes))])
	}
	return b.String()
}

func TestLogCBORMatchesGoJSON(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 1000 {
		v := randomValue(r, 0)
		var buf bytes.Buffer
		e := json.NewEncoder(&buf)
		e.SetEscapeHTML(false)
		if err := e.Encode(v); err != nil {
			t.Fatal(err)
		}
		j := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
		c, err := jsonToCBOR(j)
		if err != nil {
			t.Fatalf("%d: %s: %v", i, j, err)
		}
		// valid CBOR, the same value
		var back any
		if err := cbor.Unmarshal(c, &back); err != nil {
			t.Fatalf("%d: %s: %v", i, j, err)
		}
		got, err := cborToJSON(c)
		if err != nil {
			t.Fatalf("%d: %s: %v", i, j, err)
		}
		// byte for byte what Go's encoder writes
		if !bytes.Equal(got, j) {
			t.Fatalf("%d:\nwant %s\ngot  %s", i, j, got)
		}
	}
}

// TestLogCBORReadsSortedEncoding reads the payloads written by the first encoder of ADR 0180 §2 (fxamacker/cbor,
// deterministic: keys sorted, floats shortened).
func TestLogCBORReadsSortedEncoding(t *testing.T) {
	enc, _ := cbor.CoreDetEncOptions().EncMode()
	for _, v := range []any{
		map[string]any{"b": 1.5, "a": float32(0.25), "c": []any{uint64(math.MaxUint64), int64(-5), "x", nil, true}},
		cbor.Tag{Number: cborTagUUID, Content: bytes.Repeat([]byte{0xab}, 16)},
		1e300, -0.0, 65504.0, 1e-7,
	} {
		c, err := enc.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := cbor.Unmarshal(c, &want); err != nil {
			t.Fatal(err)
		}
		got, err := cborToJSON(c)
		if err != nil {
			t.Fatalf("%x: %v", c, err)
		}
		var g any
		if err := json.Unmarshal(got, &g); err != nil {
			t.Fatalf("%s: %v", got, err)
		}
		if fmt.Sprint(normal(want)) != fmt.Sprint(normal(g)) {
			t.Fatalf("%x: want %v, got %s", c, want, got)
		}
	}
}

func normal(v any) any {
	switch x := v.(type) {
	case map[any]any:
		m := map[string]any{}
		for k, e := range x {
			m[fmt.Sprint(k)] = normal(e)
		}
		return m
	case map[string]any:
		for k, e := range x {
			x[k] = normal(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normal(e)
		}
	case cbor.Tag:
		return string(appendUUID(nil, x.Content.([]byte)))
	case uint64:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	case string:
		return x
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func TestLogCBORRefusesInvalidJSON(t *testing.T) {
	for _, j := range []string{``, `{`, `{"a"}`, `[1,]`, `"\x01"`, `tru`, `{"a":1}x`, `"\q"`, `01x`} {
		if _, err := jsonToCBOR([]byte(j)); err == nil {
			t.Errorf("%q accepted", j)
		}
	}
}

func BenchmarkLogCBOR(b *testing.B) {
	j := entry(42)
	c, _ := jsonToCBOR(j)
	b.Run("encode", func(b *testing.B) {
		for b.Loop() {
			if _, err := jsonToCBOR(j); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode", func(b *testing.B) {
		for b.Loop() {
			if _, err := cborToJSON(c); err != nil {
				b.Fatal(err)
			}
		}
	})
}
