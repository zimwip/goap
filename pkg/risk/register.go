package risk

import "github.com/zimwip/goap/pkg/domain"

// Register makes the kinds of item of the risk register known to the graph: risk, action and waiver (see
// domain.RegisterItemKind). The services that write or read such items call it at start, and so does a test that
// builds them; it may be called any number of times.
func Register() {
	domain.RegisterItemKind(KindRisk, validateRecord)
	domain.RegisterItemKind(KindAction, validateRecord)
	domain.RegisterItemKind(KindWaiver, validateWaiver)
}
