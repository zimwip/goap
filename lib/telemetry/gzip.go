package telemetry

import (
	"compress/gzip"
	"io"
	"runtime"

	"connectrpc.com/connect"
)

// gzipFree holds the gzip writers of the answers between two uses. Connect keeps its compressors in a sync.Pool, which
// every garbage collection empties: a busy service collects several times a second, so nearly every compressed answer
// allocated a new writer (some 800 KiB of deflate state, the first allocator of the graph service under load). A writer
// the pool lets go comes back here (recycled), up to the bound, the rest is collected.
var gzipFree = make(chan *gzip.Writer, 64)

// recycled is a gzip writer handed to Connect: once Connect lets go of it, its writer returns to gzipFree.
type recycled struct{ *gzip.Writer }

func newGzipWriter() connect.Compressor {
	var w *gzip.Writer
	select {
	case w = <-gzipFree:
	default:
		w = gzip.NewWriter(io.Discard)
	}
	r := &recycled{w}
	runtime.AddCleanup(r, func(w *gzip.Writer) {
		w.Reset(io.Discard)
		select {
		case gzipFree <- w:
		default:
		}
	}, w)
	return r
}

// gzipCompression is Connect's gzip (the standard library, default level) with writers that outlive a collection.
func gzipCompression() connect.HandlerOption {
	return connect.WithCompression("gzip", func() connect.Decompressor { return &gzip.Reader{} }, newGzipWriter)
}
