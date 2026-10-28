package lnk

import (
	"context"
	"fmt"
	"strings"
)

// Kinds of source: the kind picks the resolver that reads the source.
const (
	// KindGraph: a namespace of a graph. Location is the base URL of the graph service; empty, the graph the registry
	// belongs to. Every namespace of this graph is a graph source of its own name without being declared.
	KindGraph = "graph"
	// KindLocalFS: a directory of files the service reads on its own disk, Location its root; the key of a thing is its
	// path under the root (ADR 0171).
	KindLocalFS = "local-fs"
	// KindWeb: the pages of a web site; declared, Location is its base address and the key of a page its path under it;
	// the http and https schemes are its own (an address read with no declared source).
	KindWeb = "web"
	// KindLnk is the type of source of the scheme lnk: the built-in resolver of the second level, reading
	// <source>/<type>/<key> through the source of that name. No source is of that kind.
	KindLnk = "lnk"
)

// kindAliases are the former names of kinds, still read: files was the name of local-fs (ADR 0171).
var kindAliases = map[string]string{"files": KindLocalFS}

// CanonicalKind is the name of a kind, a former name read as the kind it became.
func CanonicalKind(kind string) string {
	if k, ok := kindAliases[kind]; ok {
		return k
	}
	return kind
}

// Source is the definition of a source: its name (the source of the URLs that point into it), where it lives in terms
// of services, and what a resolver needs to read it.
type Source struct {
	// Name is the source of the URLs (lnk:<name>/...).
	Name string `json:"name"`
	// Kind picks the resolver (KindGraph, KindLocalFS, KindWeb, or a kind a composition registers).
	Kind string `json:"kind"`
	// Namespace is the namespace of the graph a graph source reads (a URL names no namespace); empty: its name.
	Namespace string `json:"namespace,omitempty"`
	// Location is where the source lives: the base URL of a graph service, the root directory of files...
	Location string `json:"location,omitempty"`
	// Namespaces are the namespaces a graph source with a location holds (ADR 0154 §9: the content graph): every call
	// and every URL naming one of them goes to that graph, the source acting as its driver.
	Namespaces []string `json:"namespaces,omitempty"`
	// Branch is the branch of the graph the source reads (empty: main).
	Branch      string `json:"branch,omitempty"`
	Description string `json:"description,omitempty"`
	// Params are what else the resolver of the kind needs, opaque to the registry.
	Params map[string]any `json:"params,omitempty"`
}

// Holds reports whether a graph source with a location holds the namespace ns (Namespaces).
func (s Source) Holds(ns string) bool {
	if s.Kind != KindGraph || s.Location == "" {
		return false
	}
	for _, n := range s.Namespaces {
		if n == ns {
			return true
		}
	}
	return false
}

// NamespaceOf returns the namespace a source reads: its Namespace, else its name.
func (s Source) NamespaceOf() string {
	if s.Namespace != "" {
		return s.Namespace
	}
	return s.Name
}

// Validate checks the definition.
func (s Source) Validate() error {
	if !sourceRE.MatchString(s.Name) {
		return fmt.Errorf("source %q: want [a-z][a-z0-9_.-]*: %w", s.Name, ErrInvalid)
	}
	switch CanonicalKind(s.Kind) {
	case "":
		return fmt.Errorf("source %q: no kind: %w", s.Name, ErrInvalid)
	case KindLnk:
		return fmt.Errorf("source %q: the kind %s is the scheme's own resolver, no source is of it: %w", s.Name, KindLnk, ErrInvalid)
	case KindLocalFS, KindWeb:
		if s.Location == "" {
			return fmt.Errorf("source %q of kind %s: no location: %w", s.Name, s.Kind, ErrInvalid)
		}
	}
	return nil
}

// Kinds of target: what a URL points to.
const (
	// TargetNode: a node of a graph (this one or another).
	TargetNode = "node"
	// TargetFile: a file; TargetDirectory: a directory.
	TargetFile      = "file"
	TargetDirectory = "directory"
)

// NodeRef is the node a target is, in the graph of its source: id and version (0: none pinned). It mirrors
// domain.NodeRef, which this leaf package cannot import.
type NodeRef struct {
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
}

// Target is what a URL points to, as its resolver reads it: the level of abstraction every source shares (a kind, a
// type, a key, a title), and what is specific to the kind (the node in its graph, the location of a file...).
type Target struct {
	// URL is the logical URL of the target (its version when the source pinned one).
	URL URL `json:"url"`
	// Kind is TargetNode, TargetFile, TargetDirectory or a kind of the resolver.
	Kind string `json:"kind"`
	// Type is the type of the target in its domain: the qualified node type of a node (alm@Requirement), the type of
	// the URL otherwise.
	Type  string `json:"type"`
	Key   string `json:"key"`
	Title string `json:"title,omitempty"`
	// Node is the node of a node target, in the graph of its source.
	Node *NodeRef `json:"node,omitempty"`
	// Location is where the target lives outside a graph (a file path, a web address).
	Location string `json:"location,omitempty"`
	// Properties are what the resolver read of the target (a node's properties, a file's size...).
	Properties map[string]any `json:"props,omitempty"`
	// Open says how a reader opens the target (ADR 0171 §5), set when the target was opened (Registry.OpenIn,
	// ResolveAddress), nil for a target only resolved.
	Open *Open `json:"open,omitempty"`
	// Content is the text of a target opened in mode OpenText.
	Content string `json:"content,omitempty"`
}

// Modes of opening a target: what a reader does with it.
const (
	// OpenNode: a node, opened in its editor (Target.Node, Target.URL).
	OpenNode = "node"
	// OpenText: a text, Target.Content, shown in Open.Language.
	OpenText = "text"
	// OpenInline: a thing a browser shows by itself, Open.Href a data: address of its bytes (an image).
	OpenInline = "inline"
	// OpenExternal: an address a browser opens in a tab of its own, Open.Href.
	OpenExternal = "external"
	// OpenPath: a thing no browser reaches (a directory, a large or binary file of a local source): Open.Href is where
	// it lives, to copy.
	OpenPath = "path"
)

// Open is how a reader opens a target.
type Open struct {
	Mode string `json:"mode"`
	// Href is the address the mode opens: the web address, the data: address, the path.
	Href string `json:"href,omitempty"`
	// Media is the media type of the thing (text/x-go, image/png...), empty when nothing tells it.
	Media string `json:"media,omitempty"`
	// Language is the language to show a text in (contracts/codetext's names), empty for plain text.
	Language string `json:"language,omitempty"`
	// Truncated reports a text cut at the size a reader is given.
	Truncated bool `json:"truncated,omitempty"`
}

// Resolver reads the sources of one kind. One resolver may serve many sources (each graph service, each root
// directory): every call names the source it is about.
type Resolver interface {
	// Resolve reads what a URL of the source points to: ErrNotFound when the source has no such thing.
	Resolve(ctx context.Context, src Source, u URL) (Target, error)
	// URLOf gives the URL of a thing of the source from what is known of it: a node (Target.Node), a location, or
	// its type and key.
	URLOf(ctx context.Context, src Source, t Target) (URL, error)
}

// ResolverFuncs makes a Resolver of two functions (tests, small kinds); without URLOfFunc a URL is the source, the
// type and the key of the target.
type ResolverFuncs struct {
	ResolveFunc func(ctx context.Context, src Source, u URL) (Target, error)
	URLOfFunc   func(ctx context.Context, src Source, t Target) (URL, error)
}

func (f ResolverFuncs) Resolve(ctx context.Context, src Source, u URL) (Target, error) {
	return f.ResolveFunc(ctx, src, u)
}

func (f ResolverFuncs) URLOf(ctx context.Context, src Source, t Target) (URL, error) {
	if f.URLOfFunc == nil {
		return New(src.Name, BareType(t.Type), t.Key), nil
	}
	return f.URLOfFunc(ctx, src, t)
}

// BareType is the name of a type without its namespace ("alm@Requirement" -> "Requirement"): the type of a URL, the
// namespace being the source's.
func BareType(t string) string {
	if _, name, ok := strings.Cut(t, "@"); ok {
		return name
	}
	return t
}
