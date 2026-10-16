package registrysvc

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/def"
)

// The client of the registry builds the activable form the registry builds (ADR 0126 step 6): the same compilation
// over the definition and the domains the version compiles against, here the version of alm it pinned at its
// publication, not the one in force since.
func TestClientActivatesAsTheRegistry(t *testing.T) {
	s := versionedService(t)
	ctx := as("admin")
	alm := func(version, extends string) def.Domain {
		d := almDomain(version)
		d.NodeTypes[1].Extends = extends
		return d
	}
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, alm("1", "Requirement")), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.importPublished(ctx, refMeth("uses")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := domainsOf(s).ImportDomain(ctx, mustYAML(t, alm("2", "")), true); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(registryv1connect.NewRegistryServiceHandler(&Handler{Service: s}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	here, err := s.Methodology(ctx, "uses@1")
	if err != nil {
		t.Fatal(err)
	}
	there, err := NewClient(srv.Client(), srv.URL).Methodology(ctx, "uses@1")
	if err != nil {
		t.Fatal(err)
	}
	if len(here.Supertypes()) == 0 || !reflect.DeepEqual(here.Supertypes(), there.Supertypes()) {
		t.Fatalf("the types of the pinned domains: registry %v, client %v", here.Supertypes(), there.Supertypes())
	}
	if here.Definition().Domains["alm"] != "1" || there.Definition().Domains["alm"] != "1" {
		t.Fatalf("pins %v %v", here.Definition().Domains, there.Definition().Domains)
	}
}
