package registrysvc

import (
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
)

func TestUnknownBuiltinRefused(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	withALM(t, s)
	m := example(t)
	m.Version = "3.0.0"
	m.Actions = append(append([]methodology.Action(nil), m.Actions...), methodology.Action{
		Name: "typo", Kind: methodology.KindBuiltin, Builtin: "nosuch", Effects: map[string]bool{m.Conditions[0].Name: true}})
	_, issues, err := s.Save(as("admin"), m)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range issues {
		found = found || (strings.HasSuffix(i.Path, ".builtin") && strings.Contains(i.Message, "nosuch"))
	}
	if !found {
		t.Fatalf("unknown builtin not reported: %v", issues)
	}
	if _, err := s.Publish(as("admin"), m.Name, m.Version); !errors.Is(err, ErrInvalid) {
		t.Fatalf("published: %v", err)
	}
}
