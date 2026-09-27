package domain

import (
	"errors"
	"fmt"
	"strings"
)

// TypeRef is the reference to a node type or a link type (ADR 0012): the namespace of the domain that declares it
// and its name in that domain. Its text form is "<namespace>@<name>" (alm@Requirement); it is the value of Node.Type
// and of Link.Type.
type TypeRef struct {
	Namespace string
	Name      string
}

// TypeSep separates the namespace and the name of a type reference.
const TypeSep = "@"

// String renders the reference ("alm@Requirement"), or the bare name when it has no namespace.
func (r TypeRef) String() string {
	if r.Namespace == "" {
		return r.Name
	}
	return r.Namespace + TypeSep + r.Name
}

// IsZero reports an empty reference.
func (r TypeRef) IsZero() bool { return r.Name == "" }

// Qualified reports a reference that names its namespace.
func (r TypeRef) Qualified() bool { return r.Namespace != "" && r.Name != "" }

// ParseTypeRef reads "<namespace>@<name>". A bare name gives a reference without namespace (Qualified is false),
// which a domain definition resolves in its own namespace.
func ParseTypeRef(s string) (TypeRef, error) {
	ns, name, ok := strings.Cut(s, TypeSep)
	if !ok {
		if s == "" {
			return TypeRef{}, fmt.Errorf("empty type reference: %w", ErrInvalidRef)
		}
		return TypeRef{Name: s}, nil
	}
	if ns == "" || name == "" || strings.Contains(name, TypeSep) {
		return TypeRef{}, fmt.Errorf("type reference %q: want <namespace>@<name>: %w", s, ErrInvalidRef)
	}
	return TypeRef{Namespace: ns, Name: name}, nil
}

// QualifyIn resolves a reference written in a definition of namespace ns: a bare name is a type of ns.
func QualifyIn(ns, s string) (TypeRef, error) {
	r, err := ParseTypeRef(s)
	if err != nil {
		return r, err
	}
	if r.Namespace == "" {
		r.Namespace = ns
	}
	return r, nil
}

// ErrInvalidRef marks a malformed type reference.
var ErrInvalidRef = errors.New("invalid type reference")
