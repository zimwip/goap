package risk

import "github.com/zimwip/goap/contracts/domain"

// Origin is the origin of the kinds of the risk register in the catalogue of item kinds (domain.DescribeItemKind).
const Origin = "risk"

// Register makes the kinds of item of the risk register known to the graph: risk, action and derogation (see
// domain.RegisterItemKind). The services that write or read such items call it at start, and so does a test that
// builds them; it may be called any number of times.
func Register() {
	domain.RegisterItemType(TypeRisk, validateRecord)
	domain.RegisterItemType(TypeAction, validateRecord)
	domain.RegisterItemType(TypeDerogation, validateDerogation)
	domain.DescribeItemType(TypeRisk, Origin, "A risk of the change: each item is a version of the record of its key (ADR 0036).")
	domain.DescribeItemType(TypeAction, Origin, "An action mitigating a risk: each item is a version of the record of its key (ADR 0036).")
	domain.DescribeItemType(TypeDerogation, Origin, "A signed waiver of a rule on a target of the change, until it expires (ADR 0075).")
	// whoever writes a derogation holds derogation:sign and is its signatory (ADR 0075)
	domain.RequireItemPermission(TypeDerogation, domain.ItemPermission{Permission: PermissionSign, SubjectField: "signatory"})
}
