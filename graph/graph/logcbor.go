package graph

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// The transcoding of a log payload between JSON and CBOR (ADR 0180 §2), in one pass each way, with no tree in between:
// the CBOR keeps the order of the JSON (the graph writes its payloads with Go's encoder, which writes a value the same
// way every time), its containers with their lengths, a UUID in its canonical lower-case form as tag 37 over 16 bytes,
// an integer as an integer (a bignum, tag 2 or 3, beyond 64 bits), any other number as a float64. Back, the JSON is
// what Go's encoder writes for those values, HTML left as is.

var errPayloadJSON = errors.New("log payload: invalid JSON")

// jsonToCBOR encodes the JSON value j in CBOR.
func jsonToCBOR(j []byte) ([]byte, error) {
	t := jsonTranscoder{in: j, out: make([]byte, 0, len(j)*3/4+8)}
	t.space()
	if err := t.value(0); err != nil {
		return nil, err
	}
	t.space()
	if t.pos != len(t.in) {
		return nil, errPayloadJSON
	}
	return t.out, nil
}

type jsonTranscoder struct {
	in  []byte
	pos int
	out []byte
	buf []byte // a string unescaped
}

const maxPayloadDepth = 256

func (t *jsonTranscoder) space() {
	for t.pos < len(t.in) {
		switch t.in[t.pos] {
		case ' ', '\t', '\n', '\r':
			t.pos++
		default:
			return
		}
	}
}

func (t *jsonTranscoder) value(depth int) error {
	if t.pos >= len(t.in) || depth > maxPayloadDepth {
		return errPayloadJSON
	}
	switch c := t.in[t.pos]; {
	case c == '{':
		return t.container(depth, '}', 5)
	case c == '[':
		return t.container(depth, ']', 4)
	case c == '"':
		s, err := t.str()
		if err != nil {
			return err
		}
		if b, ok := uuidBytes(s); ok {
			t.out = append(t.out, 0xd8, cborTagUUID, 0x50)
			t.out = append(t.out, b...)
			return nil
		}
		t.out = cborHead(t.out, 3, uint64(len(s)))
		t.out = append(t.out, s...)
		return nil
	case c == 't':
		return t.literal("true", 0xf5)
	case c == 'f':
		return t.literal("false", 0xf4)
	case c == 'n':
		return t.literal("null", 0xf6)
	case c == '-' || (c >= '0' && c <= '9'):
		return t.number()
	}
	return errPayloadJSON
}

func (t *jsonTranscoder) literal(word string, b byte) error {
	if len(t.in)-t.pos < len(word) || string(t.in[t.pos:t.pos+len(word)]) != word {
		return errPayloadJSON
	}
	t.pos += len(word)
	t.out = append(t.out, b)
	return nil
}

// container transcodes an object (major 5) or an array (major 4): its head written once its length is known.
func (t *jsonTranscoder) container(depth int, end byte, major byte) error {
	t.pos++
	at := len(t.out)
	t.out = append(t.out, 0) // the head, one byte while the length is under 24
	n := uint64(0)
	t.space()
	if t.pos < len(t.in) && t.in[t.pos] == end {
		t.pos++
	} else {
		for {
			t.space()
			if major == 5 {
				if t.pos >= len(t.in) || t.in[t.pos] != '"' {
					return errPayloadJSON
				}
				k, err := t.str()
				if err != nil {
					return err
				}
				t.out = cborHead(t.out, 3, uint64(len(k)))
				t.out = append(t.out, k...)
				t.space()
				if t.pos >= len(t.in) || t.in[t.pos] != ':' {
					return errPayloadJSON
				}
				t.pos++
				t.space()
			}
			if err := t.value(depth + 1); err != nil {
				return err
			}
			n++
			t.space()
			if t.pos >= len(t.in) {
				return errPayloadJSON
			}
			if t.in[t.pos] == ',' {
				t.pos++
				continue
			}
			if t.in[t.pos] != end {
				return errPayloadJSON
			}
			t.pos++
			break
		}
	}
	head := cborHead(make([]byte, 0, 9), major, n)
	if len(head) == 1 {
		t.out[at] = head[0]
		return nil
	}
	// a longer head: the content moves after it
	t.out = append(t.out, head[1:]...)
	copy(t.out[at+len(head):], t.out[at+1:len(t.out)-len(head)+1])
	copy(t.out[at:], head)
	return nil
}

// str reads a JSON string, unescaped (the slice is valid until the next string).
func (t *jsonTranscoder) str() ([]byte, error) {
	t.pos++ // "
	start := t.pos
	for t.pos < len(t.in) {
		c := t.in[t.pos]
		if c == '"' {
			s := t.in[start:t.pos]
			t.pos++
			if !utf8.Valid(s) {
				return nil, errPayloadJSON
			}
			return s, nil
		}
		if c == '\\' {
			return t.escaped(start)
		}
		if c < 0x20 {
			return nil, errPayloadJSON
		}
		t.pos++
	}
	return nil, errPayloadJSON
}

func (t *jsonTranscoder) escaped(start int) ([]byte, error) {
	t.buf = append(t.buf[:0], t.in[start:t.pos]...)
	for t.pos < len(t.in) {
		c := t.in[t.pos]
		switch {
		case c == '"':
			t.pos++
			if !utf8.Valid(t.buf) {
				return nil, errPayloadJSON
			}
			return t.buf, nil
		case c < 0x20:
			return nil, errPayloadJSON
		case c != '\\':
			t.buf = append(t.buf, c)
			t.pos++
			continue
		}
		if t.pos+1 >= len(t.in) {
			return nil, errPayloadJSON
		}
		e := t.in[t.pos+1]
		t.pos += 2
		switch e {
		case '"', '\\', '/':
			t.buf = append(t.buf, e)
		case 'b':
			t.buf = append(t.buf, '\b')
		case 'f':
			t.buf = append(t.buf, '\f')
		case 'n':
			t.buf = append(t.buf, '\n')
		case 'r':
			t.buf = append(t.buf, '\r')
		case 't':
			t.buf = append(t.buf, '\t')
		case 'u':
			r, ok := t.hex4()
			if !ok {
				return nil, errPayloadJSON
			}
			if utf16.IsSurrogate(r) {
				r2 := rune(-1)
				if t.pos+1 < len(t.in) && t.in[t.pos] == '\\' && t.in[t.pos+1] == 'u' {
					save := t.pos
					t.pos += 2
					if x, ok := t.hex4(); ok {
						r2 = x
					} else {
						t.pos = save
					}
				}
				if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
					r = d
				} else {
					if r2 >= 0 {
						t.pos -= 6 // not a pair: the second one read on its own
					}
					r = utf8.RuneError
				}
			}
			t.buf = utf8.AppendRune(t.buf, r)
		default:
			return nil, errPayloadJSON
		}
	}
	return nil, errPayloadJSON
}

func (t *jsonTranscoder) hex4() (rune, bool) {
	if len(t.in)-t.pos < 4 {
		return 0, false
	}
	var r rune
	for _, c := range t.in[t.pos : t.pos+4] {
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(v)
	}
	t.pos += 4
	return r, true
}

func (t *jsonTranscoder) number() error {
	start := t.pos
	integer := true
	if t.in[t.pos] == '-' {
		t.pos++
	}
	for t.pos < len(t.in) {
		c := t.in[t.pos]
		if c >= '0' && c <= '9' {
			t.pos++
			continue
		}
		if c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			integer = false
			t.pos++
			continue
		}
		break
	}
	s := string(t.in[start:t.pos])
	if integer {
		if s[0] == '-' {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				t.out = cborHead(t.out, 1, uint64(-(i + 1)))
				return nil
			}
		} else if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			t.out = cborHead(t.out, 0, u)
			return nil
		}
		b, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return errPayloadJSON
		}
		tag := uint64(2)
		if b.Sign() < 0 {
			tag = 3
			b.Neg(b).Sub(b, big.NewInt(1)) // -1 - n
		}
		t.out = cborHead(t.out, 6, tag)
		mag := b.Bytes()
		t.out = cborHead(t.out, 2, uint64(len(mag)))
		t.out = append(t.out, mag...)
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return errPayloadJSON
	}
	t.out = append(t.out, 0xfb)
	t.out = binary.BigEndian.AppendUint64(t.out, math.Float64bits(f))
	return nil
}

// cborHead appends the head of a CBOR item of major type major and argument n.
func cborHead(out []byte, major byte, n uint64) []byte {
	m := major << 5
	switch {
	case n < 24:
		return append(out, m|byte(n))
	case n <= math.MaxUint8:
		return append(out, m|24, byte(n))
	case n <= math.MaxUint16:
		return binary.BigEndian.AppendUint16(append(out, m|25), uint16(n))
	case n <= math.MaxUint32:
		return binary.BigEndian.AppendUint32(append(out, m|26), uint32(n))
	}
	return binary.BigEndian.AppendUint64(append(out, m|27), n)
}

var errPayloadCBOR = errors.New("log payload: invalid CBOR")

// cborToJSON gives back the JSON of a CBOR payload.
func cborToJSON(c []byte) ([]byte, error) {
	d := cborReader{in: c, out: make([]byte, 0, len(c)*3/2+8)}
	if err := d.value(0); err != nil {
		return nil, err
	}
	if d.pos != len(d.in) {
		return nil, errPayloadCBOR
	}
	return d.out, nil
}

type cborReader struct {
	in  []byte
	pos int
	out []byte
}

// head reads the head of an item: its major type, its additional information and its argument.
func (d *cborReader) head() (major, info byte, n uint64, err error) {
	if d.pos >= len(d.in) {
		return 0, 0, 0, errPayloadCBOR
	}
	b := d.in[d.pos]
	d.pos++
	major, info = b>>5, b&0x1f
	switch {
	case info < 24:
		return major, info, uint64(info), nil
	case info <= 27:
		size := 1 << (info - 24)
		if len(d.in)-d.pos < size {
			return 0, 0, 0, errPayloadCBOR
		}
		p := d.in[d.pos : d.pos+size]
		d.pos += size
		switch size {
		case 1:
			n = uint64(p[0])
		case 2:
			n = uint64(binary.BigEndian.Uint16(p))
		case 4:
			n = uint64(binary.BigEndian.Uint32(p))
		default:
			n = binary.BigEndian.Uint64(p)
		}
		return major, info, n, nil
	}
	return 0, 0, 0, errPayloadCBOR // indefinite lengths are never written
}

func (d *cborReader) bytes(n uint64) ([]byte, error) {
	if uint64(len(d.in)-d.pos) < n {
		return nil, errPayloadCBOR
	}
	b := d.in[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return b, nil
}

func (d *cborReader) value(depth int) error {
	if depth > maxPayloadDepth {
		return errPayloadCBOR
	}
	major, info, n, err := d.head()
	if err != nil {
		return err
	}
	switch major {
	case 0:
		d.out = strconv.AppendUint(d.out, n, 10)
	case 1:
		if n < math.MaxInt64 {
			d.out = strconv.AppendInt(d.out, -1-int64(n), 10)
		} else {
			b := new(big.Int).SetUint64(n)
			d.out = b.Neg(b.Add(b, big.NewInt(1))).Append(d.out, 10)
		}
	case 3:
		s, err := d.bytes(n)
		if err != nil {
			return err
		}
		d.out = appendJSONString(d.out, s)
	case 4:
		if n > uint64(len(d.in)-d.pos) {
			return errPayloadCBOR
		}
		d.out = append(d.out, '[')
		for i := uint64(0); i < n; i++ {
			if i > 0 {
				d.out = append(d.out, ',')
			}
			if err := d.value(depth + 1); err != nil {
				return err
			}
		}
		d.out = append(d.out, ']')
	case 5:
		if n > uint64(len(d.in)-d.pos) {
			return errPayloadCBOR
		}
		d.out = append(d.out, '{')
		for i := uint64(0); i < n; i++ {
			if i > 0 {
				d.out = append(d.out, ',')
			}
			km, _, kn, err := d.head()
			if err != nil || km != 3 {
				return errPayloadCBOR
			}
			k, err := d.bytes(kn)
			if err != nil {
				return err
			}
			d.out = append(appendJSONString(d.out, k), ':')
			if err := d.value(depth + 1); err != nil {
				return err
			}
		}
		d.out = append(d.out, '}')
	case 6:
		return d.tagged(n)
	case 7:
		return d.simple(info, n)
	default: // 2: a byte string only stands under a tag
		return errPayloadCBOR
	}
	return nil
}

func (d *cborReader) tagged(tag uint64) error {
	m, _, n, err := d.head()
	if err != nil || m != 2 {
		return errPayloadCBOR
	}
	b, err := d.bytes(n)
	if err != nil {
		return err
	}
	switch tag {
	case cborTagUUID:
		if len(b) != 16 {
			return errPayloadCBOR
		}
		d.out = append(d.out, '"')
		d.out = appendUUID(d.out, b)
		d.out = append(d.out, '"')
	case 2, 3:
		v := new(big.Int).SetBytes(b)
		if tag == 3 {
			v.Neg(v.Add(v, big.NewInt(1)))
		}
		d.out = v.Append(d.out, 10)
	default:
		return fmt.Errorf("log payload: CBOR tag %d", tag)
	}
	return nil
}

func (d *cborReader) simple(info byte, n uint64) error {
	switch info {
	case 20:
		d.out = append(d.out, "false"...)
	case 21:
		d.out = append(d.out, "true"...)
	case 22:
		d.out = append(d.out, "null"...)
	case 25:
		return d.float(float64(halfToFloat(uint16(n))))
	case 26:
		return d.float(float64(math.Float32frombits(uint32(n))))
	case 27:
		return d.float(math.Float64frombits(n))
	default:
		return errPayloadCBOR
	}
	return nil
}

// float writes f as Go's JSON encoder does.
func (d *cborReader) float(f float64) error {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return errPayloadCBOR
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	start := len(d.out)
	d.out = strconv.AppendFloat(d.out, f, format, -1, 64)
	if format == 'e' {
		// clean up e-09 to e-9
		n := len(d.out) - start
		if n >= 4 && d.out[len(d.out)-4] == 'e' && d.out[len(d.out)-3] == '-' && d.out[len(d.out)-2] == '0' {
			d.out[len(d.out)-2] = d.out[len(d.out)-1]
			d.out = d.out[:len(d.out)-1]
		}
	}
	return nil
}

// halfToFloat reads an IEEE 754 half-precision float.
func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h & 0x3ff)
	switch {
	case exp == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	case exp == 0:
		if frac == 0 {
			return math.Float32frombits(sign)
		}
		f := float32(frac) / 1024 / 16384 // 2^-14
		if sign != 0 {
			return -f
		}
		return f
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}

func appendUUID(out, b []byte) []byte {
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
	return append(out, s[:]...)
}

const hexDigits = "0123456789abcdef"

// appendJSONString writes s as a JSON string, as Go's encoder does with HTML left as is.
func appendJSONString(out, s []byte) []byte {
	out = append(out, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			out = append(out, s[start:i]...)
			switch c {
			case '"', '\\':
				out = append(out, '\\', c)
			case '\b':
				out = append(out, '\\', 'b')
			case '\f':
				out = append(out, '\\', 'f')
			case '\n':
				out = append(out, '\\', 'n')
			case '\r':
				out = append(out, '\\', 'r')
			case '\t':
				out = append(out, '\\', 't')
			default:
				out = append(out, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRune(s[i:])
		if r == utf8.RuneError && size == 1 {
			out = append(out, s[start:i]...)
			out = append(out, `�`...)
			i += size
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			out = append(out, s[start:i]...)
			out = append(out, '\\', 'u', '2', '0', '2', hexDigits[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	out = append(out, s[start:]...)
	return append(out, '"')
}
