// Package lnk names what a link points to as a logical URL (ADR 0154): lnk:<source>/<type>/<key>.
//
// A source is a place things live in: a namespace of a graph (this one, or another one: federation), a directory of
// files, anything a resolver knows how to read. A thing of a source is a type/key tuple:
// the type says what it is in the source (a node type of its domain, File, ...), the key which one. The namespace is a
// property of the source, never of the URL: a node type alm@Requirement of this graph is lnk:alm/Requirement/<key>.
//
// The package is a leaf of the contracts: it knows no graph. A Resolver turns a URL into a Target (what it points to)
// and a Target back into its URL; a Registry holds the sources and their resolvers by kind.
package lnk

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Scheme is the scheme of a logical URL.
const Scheme = "lnk"

// Errors of the package.
var (
	// ErrInvalid: a malformed URL or source definition.
	ErrInvalid = errors.New("invalid lnk")
	// ErrUnknownSource: no source of that name, and no namespace of this graph either.
	ErrUnknownSource = errors.New("unknown lnk source")
	// ErrNoResolver: a source whose kind has no resolver in this process.
	ErrNoResolver = errors.New("no resolver for the lnk source")
	// ErrNotFound: the source knows no thing of that type and key.
	ErrNotFound = errors.New("lnk target not found")
)

var (
	sourceRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}$`)
	typeRE   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,126}$`)
)

// URL is a logical URL: the source, the type and the key of what it names, and optionally an exact version of it
// (0: the thing as the reader sees it, the logical identity; a version pins one, for the sources that keep versions).
type URL struct {
	Source  string
	Type    string
	Key     string
	Version int
}

// New returns the URL of a type/key tuple of a source.
func New(source, typ, key string) URL { return URL{Source: source, Type: typ, Key: key} }

// IsZero reports an unset URL.
func (u URL) IsZero() bool { return u.Source == "" && u.Type == "" && u.Key == "" }

// Logical returns the URL without its version: the identity of the thing, whatever its version.
func (u URL) Logical() URL { u.Version = 0; return u }

// At returns the URL pinned to a version.
func (u URL) At(v int) URL { u.Version = v; return u }

// Validate checks the parts of the URL.
func (u URL) Validate() error {
	switch {
	case !sourceRE.MatchString(u.Source):
		return fmt.Errorf("source %q: want [a-z][a-z0-9_.-]*: %w", u.Source, ErrInvalid)
	case !typeRE.MatchString(u.Type):
		return fmt.Errorf("type %q: want [A-Za-z][A-Za-z0-9_.-]*: %w", u.Type, ErrInvalid)
	case u.Key == "":
		return fmt.Errorf("empty key: %w", ErrInvalid)
	case u.Version < 0:
		return fmt.Errorf("version %d: %w", u.Version, ErrInvalid)
	}
	return nil
}

// String renders lnk:<source>/<type>/<key>[?v=<version>]. The key keeps its slashes (a path is a key); every other
// character a URL path segment cannot carry as is is percent-encoded, so the text round-trips through Parse.
func (u URL) String() string {
	if u.IsZero() {
		return ""
	}
	var b strings.Builder
	b.WriteString(Scheme)
	b.WriteByte(':')
	b.WriteString(u.Source)
	b.WriteByte('/')
	b.WriteString(u.Type)
	b.WriteByte('/')
	b.WriteString(escapeKey(u.Key))
	if u.Version > 0 {
		b.WriteString("?v=")
		b.WriteString(strconv.Itoa(u.Version))
	}
	return b.String()
}

// MarshalText renders the URL as its text, so a URL is a string in JSON.
func (u URL) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

// UnmarshalText reads the text of a URL (empty: the zero URL).
func (u *URL) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*u = URL{}
		return nil
	}
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*u = p
	return nil
}

// Parse reads lnk:<source>/<type>/<key>[?v=<version>].
func Parse(s string) (URL, error) {
	rest, ok := strings.CutPrefix(s, Scheme+":")
	if !ok {
		return URL{}, fmt.Errorf("%q: want %s:<source>/<type>/<key>: %w", s, Scheme, ErrInvalid)
	}
	rest, query, _ := strings.Cut(rest, "?")
	src, rest, ok1 := strings.Cut(rest, "/")
	typ, key, ok2 := strings.Cut(rest, "/")
	if !ok1 || !ok2 {
		return URL{}, fmt.Errorf("%q: want %s:<source>/<type>/<key>: %w", s, Scheme, ErrInvalid)
	}
	k, err := url.PathUnescape(key)
	if err != nil {
		return URL{}, fmt.Errorf("%q: key: %w", s, ErrInvalid)
	}
	u := URL{Source: src, Type: typ, Key: k}
	if query != "" {
		q, err := url.ParseQuery(query)
		if err != nil {
			return URL{}, fmt.Errorf("%q: query: %w", s, ErrInvalid)
		}
		for name := range q {
			if name != "v" {
				return URL{}, fmt.Errorf("%q: unknown parameter %q: %w", s, name, ErrInvalid)
			}
		}
		v, err := strconv.Atoi(q.Get("v"))
		if err != nil || v < 1 {
			return URL{}, fmt.Errorf("%q: version %q: %w", s, q.Get("v"), ErrInvalid)
		}
		u.Version = v
	}
	if err := u.Validate(); err != nil {
		return URL{}, fmt.Errorf("%q: %w", s, err)
	}
	return u, nil
}

// MustParse is Parse for constants and tests: it panics on an error.
func MustParse(s string) URL {
	u, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// IsURL reports a text that has the scheme of a logical URL (well formed or not).
func IsURL(s string) bool { return strings.HasPrefix(s, Scheme+":") }

func escapeKey(k string) string {
	parts := strings.Split(k, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
