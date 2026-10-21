package cache

// Estimates of the memory a value holds, for the size function of a cache: a guess of the right order, never a
// measure.
const (
	// WordBytes is a pointer, an integer, a slice or string header word.
	WordBytes = 16
	// SlotBytes is an entry of a map, besides its key and value.
	SlotBytes = 48
	// StructBytes is a struct of a few fields.
	StructBytes = 160
)

// Of estimates a decoded JSON-like value: strings, maps, slices and scalars.
func Of(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case string:
		return WordBytes + int64(len(x))
	case []byte:
		return WordBytes + int64(len(x))
	case map[string]any:
		n := int64(SlotBytes)
		for k, e := range x {
			n += SlotBytes + int64(len(k)) + Of(e)
		}
		return n
	case []any:
		n := int64(WordBytes)
		for _, e := range x {
			n += WordBytes + Of(e)
		}
		return n
	case []string:
		n := int64(WordBytes)
		for _, e := range x {
			n += WordBytes + int64(len(e))
		}
		return n
	default:
		return WordBytes
	}
}
