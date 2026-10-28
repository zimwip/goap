package graphapi

import "github.com/zimwip/goap/contracts/domain"

// NewBranch describes a branch to open from a commit (CreateBranch).
type NewBranch struct {
	Name string
	// Namespace the branch belongs to (default: domain.DefaultNamespace).
	Namespace   string
	From        domain.CommitID
	Origin      string // change / option that opens the branch
	Description string
}

// MergeCandidate is a node changed on the source branch since its fork.
type MergeCandidate struct {
	Node     domain.NodeID   `json:"node"`
	Key      string          `json:"key"`
	Type     string          `json:"type"`
	Kind     string          `json:"kind"`
	Ancestor *domain.NodeRef `json:"ancestor,omitempty"`
	// Ours is the version on the target branch (nil when added or deleted there).
	Ours   *domain.NodeRef `json:"ours,omitempty"`
	Theirs domain.NodeRef  `json:"theirs"`
	// Deleted is set when the source branch deleted the node.
	Deleted   bool           `json:"deleted,omitempty"`
	Base      map[string]any `json:"base,omitempty"`
	OursProps map[string]any `json:"oursProps,omitempty"`
	// TheirsProps and Merged are the source and proposed merged properties.
	TheirsProps map[string]any `json:"theirsProps,omitempty"`
	Merged      map[string]any `json:"merged,omitempty"`
	Conflicts   []string       `json:"conflicts,omitempty"`
}

// MergePlan lists what merging a branch into another would do.
type MergePlan struct {
	From       string           `json:"from"`
	Into       string           `json:"into"`
	IntoHead   domain.CommitID  `json:"intoHead"`
	Candidates []MergeCandidate `json:"candidates"`
}

// Resolution resolves a merge conflict: Props is the resolved property map
// (for a deletion conflict, any resolution with Skip unset applies the
// deletion); Skip keeps the target version unchanged.
type Resolution struct {
	Props map[string]any `json:"props,omitempty"`
	Skip  bool           `json:"skip,omitempty"`
}

// ImpactConflicts are the conflicts a rebase left on a change impact, not settled yet.
type ImpactConflicts struct {
	Impact    domain.ChangeImpactID `json:"impactId"`
	Key       string                `json:"key"`
	Conflicts []string              `json:"conflicts"`
}

// RebaseState is where a change stands against the branch it was forked from (ADR 0101, 0103): the change impacts whose
// node that branch changed since the fork, a rebase bringing them up to date, and the conflicts left to settle.
type RebaseState struct {
	Parent    domain.ChangeID         `json:"parentId"`
	Behind    []domain.ChangeImpactID `json:"behind,omitempty"`
	Conflicts []ImpactConflicts       `json:"conflicts,omitempty"`
	// Branch and Onto, for a change behind the head of the branch it was forked from: that branch and the head commit a
	// rebase moves it to (empty: not behind).
	Branch string          `json:"branch,omitempty"`
	Onto   domain.CommitID `json:"onto,omitempty"`
}

// Conflicting returns the candidates that need a resolution.
func (p MergePlan) Conflicting() []MergeCandidate {
	var out []MergeCandidate
	for _, c := range p.Candidates {
		if len(c.Conflicts) > 0 {
			out = append(out, c)
		}
	}
	return out
}
