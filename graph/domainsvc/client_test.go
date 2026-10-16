package domainsvc

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/domainclient"
)

// The client reads what the service serves, keeps it until invalidated, and answers the reads of a methodology
// registry (ADR 0125 §3).
func TestClient(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	withALM(t, s)
	path, h := registryv1connect.NewDomainServiceHandler(&Handler{Service: s})
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := domainclient.NewClient(srv.Client(), srv.URL)
	ctx := t.Context()
	ds, err := c.Domains(ctx)
	if err != nil || len(ds) != 1 || ds[0].Name != "alm" {
		t.Fatalf("domains in force: %v %v", ds, err)
	}
	all, err := c.DomainVersions(ctx, true)
	if err != nil || len(all) != 4 || !all[0].Builtin {
		t.Fatalf("every version, the built-in ones first: %d %v", len(all), err)
	}
	alm, err := c.GetDomain(ctx, "alm", "")
	if err != nil || alm.Status != def.StatusPublished || alm.PublishedAt.IsZero() || len(alm.Domain.NodeTypes) == 0 {
		t.Fatalf("latest published: %+v %v", alm, err)
	}
	if _, err := c.GetDomain(ctx, "alm", "0.0.1"); !errors.Is(err, domainclient.ErrNotFound) {
		t.Fatalf("unknown version: %v", err)
	}

	next := alm.Domain
	next.Version = "99.0.0"
	if _, _, err := s.SaveDomain(as("admin"), next); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDomain(as("admin"), "alm", "99.0.0"); err != nil {
		t.Fatal(err)
	}
	if r, _ := c.GetDomain(ctx, "alm", ""); r.Domain.Version == "99.0.0" {
		t.Fatal("the client keeps what it read until invalidated")
	}
	c.Invalidate()
	if r, err := c.GetDomain(ctx, "alm", ""); err != nil || r.Domain.Version != "99.0.0" {
		t.Fatalf("read again: %+v %v", r.Domain.Version, err)
	}
}
