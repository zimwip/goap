package risk

import "github.com/zimwip/goap/pkg/domain"

// Register makes the kinds of item of the risk register known to the graph: risk, action, waiver and derogation (see
// domain.RegisterItemKind). The services that write or read such items call it at start, and so does a test that
// builds them; it may be called any number of times.
func Register() {
	domain.RegisterItemKind(KindRisk, validateRecord)
	domain.RegisterItemKind(KindAction, validateRecord)
	domain.RegisterItemKind(KindWaiver, validateWaiver)
	domain.RegisterItemKind(KindDerogation, validateDerogation)
	// whoever writes a derogation holds derogation:sign and is its signatory (ADR 0075)
	domain.RequireItemPermission(KindDerogation, domain.ItemPermission{Permission: PermissionSign, SubjectField: "signatory"})
}
