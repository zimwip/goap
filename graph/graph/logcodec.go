package graph

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/klauspost/compress/dict"
	"github.com/klauspost/compress/zstd"
)

// The payload of a log entry as stored (ADR 0180 §2): the entry's JSON (what the graph, its readers and its clients
// handle) is written as CBOR (RFC 8949, deterministic encoding; UUIDs as 16-byte strings, tag 37), then compressed in a
// zstd frame with the dictionary of its entry type when there is one. The first byte says which:
//
//	0x01  CBOR as is
//	0x02  a zstd frame, its magic number left out; the frame names its dictionary (none: 0)
//	else  JSON (an entry written before ADR 0180 §2)
//
// A dictionary is trained per entry type from the entries already written (logTrainAt), stored immutable in
// log_dictionary and named by the frames compressed with it: a new one applies to the entries written after it, never
// rewriting the others. No SQL reads inside a payload.
const (
	payloadCBOR = 0x01
	payloadZstd = 0x02
	// cborTagUUID is the CBOR tag of a UUID (16 bytes)
	cborTagUUID = 37
)

// zstdMagic starts every zstd frame: left out of a stored payload, which its first byte names already.
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

var (
	cborEnc cbor.EncMode
	cborDec cbor.DecMode
)

func init() {
	var err error
	if cborEnc, err = cbor.CoreDetEncOptions().EncMode(); err != nil {
		panic(err)
	}
	if cborDec, err = (cbor.DecOptions{
		DefaultMapType:   reflect.TypeOf(map[string]any(nil)),
		MaxNestedLevels:  256,
		MaxArrayElements: 1 << 27,
		MaxMapPairs:      1 << 27,
	}).DecMode(); err != nil {
		panic(err)
	}
}

// jsonToCBOR encodes the JSON value j in deterministic CBOR, its UUIDs as tag 37.
func jsonToCBOR(j []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(j))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("log payload: %w", err)
	}
	if d.More() {
		return nil, errors.New("log payload: more than one JSON value")
	}
	v, err := toCBORValue(v)
	if err != nil {
		return nil, err
	}
	return cborEnc.Marshal(v)
}

func toCBORValue(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			c, err := toCBORValue(e)
			if err != nil {
				return nil, err
			}
			x[k] = c
		}
	case []any:
		for i, e := range x {
			c, err := toCBORValue(e)
			if err != nil {
				return nil, err
			}
			x[i] = c
		}
	case string:
		if b, ok := uuidBytes(x); ok {
			return cbor.Tag{Number: cborTagUUID, Content: b}, nil
		}
	case json.Number:
		return numberValue(string(x))
	}
	return v, nil
}

// numberValue is a JSON number as the CBOR value that gives it back: an integer as an integer (a big one as a bignum),
// any other as a float64 (what Go's encoder wrote it from).
func numberValue(s string) (any, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	if u, err := strconv.ParseUint(s, 10, 64); err == nil {
		return u, nil
	}
	if b, ok := new(big.Int).SetString(s, 10); ok {
		return b, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("log payload: number %q: %w", s, err)
	}
	return f, nil
}

// uuidBytes reads a UUID in its canonical form, lower case: only that form is given back as is.
func uuidBytes(s string) ([]byte, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return nil, false
	}
	var b [16]byte
	j := 0
	for i := 0; i < 36; {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			i++
			continue
		}
		hi, ok1 := lowerHex(s[i])
		lo, ok2 := lowerHex(s[i+1])
		if !ok1 || !ok2 {
			return nil, false
		}
		b[j] = hi<<4 | lo
		j++
		i += 2
	}
	return b[:], true
}

func lowerHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

func uuidString(b []byte) string {
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:36], b[10:16])
	return string(s[:])
}

// cborToJSON gives back the JSON of a CBOR payload.
func cborToJSON(c []byte) ([]byte, error) {
	var v any
	if err := cborDec.Unmarshal(c, &v); err != nil {
		return nil, fmt.Errorf("log payload: %w", err)
	}
	v, err := fromCBORValue(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, fmt.Errorf("log payload: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

func fromCBORValue(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			c, err := fromCBORValue(e)
			if err != nil {
				return nil, err
			}
			x[k] = c
		}
	case []any:
		for i, e := range x {
			c, err := fromCBORValue(e)
			if err != nil {
				return nil, err
			}
			x[i] = c
		}
	case cbor.Tag:
		if b, ok := x.Content.([]byte); ok && x.Number == cborTagUUID && len(b) == 16 {
			return uuidString(b), nil
		}
		return nil, fmt.Errorf("log payload: CBOR tag %d", x.Number)
	case big.Int:
		return &x, nil
	case []byte:
		return nil, errors.New("log payload: a CBOR byte string outside a UUID")
	}
	return v, nil
}

// logDict is a dictionary of log_dictionary: its id (the frames' dictionary id), its entry type and its bytes.
type logDict struct {
	ID   uint32
	Type string
	Dict []byte
}

// logDictStore is where a repository keeps its dictionaries.
type logDictStore interface {
	// dicts reads the dictionaries of an id greater than after, by id
	dicts(ctx context.Context, after uint32) ([]logDict, error)
	// samples reads the payloads of the last n entries of an entry type, as stored
	samples(ctx context.Context, typ string, n int) ([][]byte, error)
	// addDict trains a dictionary with build, given its id, and stores it
	addDict(ctx context.Context, typ string, build func(id uint32) ([]byte, error)) (logDict, error)
}

var (
	// logTrainAt are the numbers of entries of a type a process writes without a dictionary, then since its last one,
	// before it trains the next: a first one early, a second on a larger history; none after the last
	logTrainAt = []int{1000, 50000}
	// logTrainSamples is the number of the last entries of its type a dictionary is trained from
	logTrainSamples = 2000
	// logDictBytes is the size of a dictionary
	logDictBytes = 16 << 10
	// logTrainTimeout bounds a training
	logTrainTimeout = time.Minute
)

// logCodec encodes and decodes the payloads of a repository with its dictionaries.
type logCodec struct {
	store logDictStore

	mu       sync.RWMutex
	loaded   bool
	last     uint32
	known    map[uint32]logDict
	latest   map[string]uint32 // entry type -> its newest dictionary
	encoders map[uint32]*zstd.Encoder
	decoder  *zstd.Decoder
	// written counts the entries of a type written since its newest dictionary (or since none)
	written  map[string]int
	trained  map[string]int // entry type -> the number of its dictionaries
	training map[string]bool
	wg       sync.WaitGroup
}

var plainEncoder = func() *zstd.Encoder {
	e, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderCRC(false))
	if err != nil {
		panic(err)
	}
	return e
}()

func newLogCodec(store logDictStore) *logCodec {
	d, err := zstd.NewReader(nil)
	if err != nil {
		panic(err)
	}
	return &logCodec{
		store: store, known: map[uint32]logDict{}, latest: map[string]uint32{}, encoders: map[uint32]*zstd.Encoder{},
		decoder: d, written: map[string]int{}, trained: map[string]int{}, training: map[string]bool{},
	}
}

// load reads the dictionaries the codec does not know yet; store is where (nil: the codec's).
func (c *logCodec) load(ctx context.Context, store logDictStore) error {
	if store == nil {
		store = c.store
	}
	if store == nil {
		c.mu.Lock()
		c.loaded = true
		c.mu.Unlock()
		return nil
	}
	c.mu.RLock()
	after := c.last
	c.mu.RUnlock()
	ds, err := store.dicts(ctx, after)
	if err != nil {
		return fmt.Errorf("log dictionaries: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = true
	return c.installLocked(ds...)
}

func (c *logCodec) installLocked(ds ...logDict) error {
	added := false
	for _, d := range ds {
		if _, ok := c.known[d.ID]; ok {
			continue
		}
		e, err := zstd.NewWriter(nil, zstd.WithEncoderDict(d.Dict), zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithEncoderCRC(false), zstd.WithEncoderConcurrency(2))
		if err != nil {
			return fmt.Errorf("log dictionary %d: %w", d.ID, err)
		}
		c.known[d.ID], c.encoders[d.ID] = d, e
		if d.ID > c.latest[d.Type] {
			c.latest[d.Type] = d.ID
			c.written[d.Type] = 0
		}
		c.trained[d.Type]++
		c.last = max(c.last, d.ID)
		added = true
	}
	if !added {
		return nil
	}
	dicts := make([][]byte, 0, len(c.known))
	for _, d := range c.known {
		dicts = append(dicts, d.Dict)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDicts(dicts...))
	if err != nil {
		return fmt.Errorf("log dictionaries: %w", err)
	}
	c.decoder = dec // the previous one stays with the decodes in flight
	return nil
}

// encode stores the payload j of an entry of type typ; store reads the dictionaries the first time (nil: the codec's).
func (c *logCodec) encode(ctx context.Context, store logDictStore, typ string, j []byte) ([]byte, error) {
	if len(j) == 0 {
		return []byte{}, nil
	}
	c.mu.RLock()
	loaded := c.loaded
	c.mu.RUnlock()
	if !loaded {
		if err := c.load(ctx, store); err != nil {
			return nil, err
		}
	}
	raw, err := jsonToCBOR(j)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	enc := c.encoders[c.latest[typ]]
	c.written[typ]++
	train := c.wantsTraining(typ)
	c.mu.Unlock()
	if train {
		c.train(typ)
	}
	return frame(enc, raw), nil
}

// frame is the stored form of the CBOR raw: compressed when that is shorter.
func frame(enc *zstd.Encoder, raw []byte) []byte {
	if enc == nil {
		enc = plainEncoder
	}
	z := enc.EncodeAll(raw, make([]byte, 0, len(raw)))
	if bytes.HasPrefix(z, zstdMagic) && len(z)-len(zstdMagic) < len(raw) {
		z[len(zstdMagic)-1] = payloadZstd
		return z[len(zstdMagic)-1:]
	}
	out := make([]byte, 1+len(raw))
	out[0] = payloadCBOR
	copy(out[1:], raw)
	return out
}

// raw gives back the CBOR of a stored payload, nil for a JSON one; store reads a dictionary it does not know (nil:
// the codec's).
func (c *logCodec) raw(ctx context.Context, store logDictStore, p []byte) ([]byte, error) {
	if len(p) == 0 {
		return nil, nil
	}
	switch p[0] {
	case payloadCBOR:
		return p[1:], nil
	case payloadZstd:
	default:
		return nil, nil
	}
	z := make([]byte, 0, len(zstdMagic)+len(p)-1)
	z = append(append(z, zstdMagic...), p[1:]...)
	for attempt := 0; ; attempt++ {
		c.mu.RLock()
		dec := c.decoder
		c.mu.RUnlock()
		out, err := dec.DecodeAll(z, nil)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, zstd.ErrUnknownDictionary) || attempt > 0 {
			return nil, fmt.Errorf("log payload: %w", err)
		}
		// a dictionary another process trained since
		if err := c.load(ctx, store); err != nil {
			return nil, err
		}
	}
}

// decode gives back the JSON of a stored payload.
func (c *logCodec) decode(ctx context.Context, store logDictStore, p []byte) (json.RawMessage, error) {
	if len(p) == 0 {
		return nil, nil
	}
	if p[0] != payloadCBOR && p[0] != payloadZstd {
		return json.RawMessage(p), nil // JSON
	}
	raw, err := c.raw(ctx, store, p)
	if err != nil {
		return nil, err
	}
	j, err := cborToJSON(raw)
	return json.RawMessage(j), err
}

// wantsTraining says whether the entries of typ written call for a dictionary, and if so marks it in training.
func (c *logCodec) wantsTraining(typ string) bool {
	n := c.trained[typ]
	if c.store == nil || c.training[typ] || n >= len(logTrainAt) || c.written[typ] < logTrainAt[n] {
		return false
	}
	c.training[typ] = true
	return true
}

// train trains a dictionary for typ in the background, then uses it.
func (c *logCodec) train(typ string) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), logTrainTimeout)
		defer cancel()
		d, err := c.trainNow(ctx, typ)
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.training, typ)
		if err == nil {
			err = c.installLocked(d)
		}
		if err != nil {
			c.written[typ] = 0 // try again after as many entries
			slog.Warn("graph: log dictionary not trained", "type", typ, "err", err)
		}
	}()
}

// trainNow trains and stores a dictionary of typ from its last entries.
func (c *logCodec) trainNow(ctx context.Context, typ string) (logDict, error) {
	stored, err := c.store.samples(ctx, typ, logTrainSamples)
	if err != nil {
		return logDict{}, err
	}
	samples := make([][]byte, 0, len(stored))
	for _, p := range stored {
		raw, err := c.raw(ctx, nil, p)
		if err != nil {
			return logDict{}, err
		}
		if len(raw) > 0 {
			samples = append(samples, raw)
		}
	}
	return c.store.addDict(ctx, typ, func(id uint32) ([]byte, error) { return buildLogDict(id, samples) })
}

// buildLogDict trains a dictionary of id from samples.
func buildLogDict(id uint32, samples [][]byte) (d []byte, err error) {
	if len(samples) < 8 {
		return nil, errors.New("too few entries")
	}
	defer func() {
		if r := recover(); r != nil { // the builder panics on samples it cannot use
			d, err = nil, fmt.Errorf("training: %v", r)
		}
	}()
	return dict.BuildZstdDict(samples, dict.Options{MaxDictSize: logDictBytes, HashBytes: 6, ZstdDictID: id, ZstdLevel: zstd.SpeedFastest})
}

// wait waits for the trainings in flight (tests).
func (c *logCodec) wait() { c.wg.Wait() }
