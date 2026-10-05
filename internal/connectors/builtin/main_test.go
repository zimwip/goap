package builtin_test

import (
	"os"
	"testing"

	"github.com/zimwip/goap/pkg/review"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// TestMain registers the kinds of item of the risk register, as the services do at start (ADR 0065).
func TestMain(m *testing.M) {
	risk.Register()
	verify.Register()
	review.Register()
	os.Exit(m.Run())
}
