package registrysvc

import (
	"os"
	"testing"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/contracts/verify"
)

// TestMain registers the kinds of item of the risk register, as the services do at start (ADR 0065).
func TestMain(m *testing.M) {
	risk.Register()
	assess.Register()
	verify.Register()
	os.Exit(m.Run())
}
