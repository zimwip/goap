package risk

import "github.com/zimwip/goap/contracts/domain"

// Register gives the types of item of the risk register their validation: risk, action, derogation and hypothesis (declared by
// the built-in methodology domain, ADR 0128; see domain.RegisterItemType). The services that write or read such items call it at start, and so does a test that
// builds them; it may be called any number of times.
func Register() {
	domain.RegisterItemType(TypeRisk, validateRecord)
	domain.RegisterItemType(TypeAction, validateRecord)
	domain.RegisterItemType(TypeDerogation, validateDerogation)
	domain.RegisterItemType(TypeHypothesis, validateHypothesis)
	// whoever writes a derogation holds derogation:sign and is its signatory (ADR 0075)
	domain.RequireItemPermission(TypeDerogation, domain.ItemPermission{Permission: PermissionSign, SubjectField: "signatory"})
}
