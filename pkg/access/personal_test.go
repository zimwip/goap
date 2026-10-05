package access_test

import (
	"testing"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
)

func TestPersonalUnit(t *testing.T) {
	if access.PersonalUnit("alice") != access.UserKey("alice") {
		t.Fatal("the personal unit is the user node")
	}
	if !access.IsPersonalUnit("USR:alice") || access.IsPersonalUnit("ORG-DEFAULT") {
		t.Fatal("IsPersonalUnit")
	}
	if access.PersonalSubject("USR:alice") != "alice" || access.PersonalSubject("ORG-DEFAULT") != "" {
		t.Fatal("PersonalSubject")
	}
	c := domain.Change{OwnerOrg: access.PersonalUnit("alice")}
	if !access.IsPersonal(c) || !access.IsPersonalTo(c, "alice") || access.IsPersonalTo(c, "bob") || access.IsPersonalTo(c, "") {
		t.Fatal("change personality")
	}
	if access.IsPersonal(domain.Change{OwnerOrg: "ORG-DEFAULT"}) {
		t.Fatal("a shared change is not personal")
	}
}
