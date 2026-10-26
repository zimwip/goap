package domain

import (
	"encoding/json"
	"sync"
)

// The facets of a blackboard travel as the JSON of their values (ADR 0168: the graph sends the blackboard of a change
// to the service that judges it); a reader gets back the type it expects through the decoder registered for the
// facet's name. A facet no decoder knows is read as plain JSON values.
var (
	facetMu       sync.RWMutex
	facetDecoders = map[string]func([]byte) (any, error){}
)

// RegisterFacet declares the type of a facet, so that a blackboard read from the wire holds the facet as a T.
func RegisterFacet[T any](name string) {
	facetMu.Lock()
	defer facetMu.Unlock()
	facetDecoders[name] = func(raw []byte) (any, error) {
		var v T
		err := json.Unmarshal(raw, &v)
		return v, err
	}
}

// EncodeFacets is the JSON of each facet of a blackboard.
func EncodeFacets(facets map[string]any) (map[string][]byte, error) {
	out := make(map[string][]byte, len(facets))
	for name, v := range facets {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[name] = raw
	}
	return out, nil
}

// DecodeFacets reads facets from their JSON, each as the type registered for its name.
func DecodeFacets(raw map[string][]byte) (map[string]any, error) {
	facetMu.RLock()
	defer facetMu.RUnlock()
	out := make(map[string]any, len(raw))
	for name, b := range raw {
		decode, ok := facetDecoders[name]
		if !ok {
			decode = func(b []byte) (any, error) {
				var v any
				err := json.Unmarshal(b, &v)
				return v, err
			}
		}
		v, err := decode(b)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func init() { RegisterFacet[[]Dependency](FacetDependencies) }
