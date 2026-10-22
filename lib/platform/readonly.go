package platform

import (
	"context"
	"net/http"
	"strings"
)

type readOnlyKey struct{}

// ReadOnly marks a context whose work only reads: a store with a pool of readers (OpenSQLiteReader) serves its
// transactions there, so they never wait behind the writer of the local mode's single connection.
func ReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyKey{}, true)
}

// Writing clears the mark of ReadOnly: what follows writes.
func Writing(ctx context.Context) context.Context {
	if !IsReadOnly(ctx) {
		return ctx
	}
	return context.WithValue(ctx, readOnlyKey{}, false)
}

// IsReadOnly tells a context marked by ReadOnly.
func IsReadOnly(ctx context.Context) bool {
	v, _ := ctx.Value(readOnlyKey{}).(bool)
	return v
}

// readPrefixes are the names of the Connect procedures that only read.
var readPrefixes = []string{"Get", "List", "Export", "Diff", "Search", "Compare", "Is", "Watch", "Query"}

// ReadOnlyRequests marks the context of the requests that only read (ReadOnly): a GET, or a Connect procedure
// whose method is named Get…, List…, Export…, Diff…, Search…, Compare…, Is…, Watch… or Query…. A store still runs a write
// such a request makes on its writer.
func ReadOnlyRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if readsOnly(r) {
			r = r.WithContext(ReadOnly(r.Context()))
		}
		next.ServeHTTP(w, r)
	})
}

func readsOnly(r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
	for _, p := range readPrefixes {
		if rest, ok := strings.CutPrefix(method, p); ok && (rest == "" || rest[0] >= 'A' && rest[0] <= 'Z') {
			return true
		}
	}
	return false
}
