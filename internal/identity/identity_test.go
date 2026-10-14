package identity

import (
	"net/http"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
)

// The end user a platform service acts for travels with its identity (ADR 0114); a person never acts for another.
func TestOnBehalfOfTravelsForPlatformServicesOnly(t *testing.T) {
	h := http.Header{}
	SetHeaders(authz.System("trigger:nightly").For("alice"), h)
	if p := FromHeaders(h); p.Subject != "system:trigger:nightly" || p.OnBehalfOf != "alice" || p.EndUser() != "alice" {
		t.Fatalf("a service on behalf of alice: %+v", p)
	}
	SetHeaders(authz.Principal{Subject: "bob"}, h)
	if h.Get(HeaderOnBehalfOf) != "" {
		t.Fatal("a person's headers carry no end user of a previous principal")
	}
	h.Set(HeaderOnBehalfOf, "alice")
	if p := FromHeaders(h); p.OnBehalfOf != "" || p.EndUser() != "bob" {
		t.Fatalf("a person acts for themselves: %+v", p)
	}
}
