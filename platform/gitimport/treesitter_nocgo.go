//go:build !cgo

package gitimport

import "errors"

// TreeSitterAvailable tells whether this build reads the symbols of the sources (Tree-sitter needs cgo: the images of
// the platform are built without it).
const TreeSitterAvailable = false

// TreeSitter is not available without cgo.
type TreeSitter struct{}

// NewTreeSitter refuses: this build has no Tree-sitter.
func NewTreeSitter() (*TreeSitter, error) {
	return nil, errors.New("tree-sitter: this build has no cgo, the symbols cannot be read")
}

// Close does nothing.
func (t *TreeSitter) Close() {}

// Language names the language of a file by its extension.
func (t *TreeSitter) Language(name string) string { return languageOf(name) }

// Symbols refuses.
func (t *TreeSitter) Symbols(string, []byte) ([]Symbol, error) {
	return nil, errors.New("tree-sitter: not in this build")
}
