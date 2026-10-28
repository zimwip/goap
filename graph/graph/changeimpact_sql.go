package graph

import (
	"encoding/json"

	"github.com/zimwip/goap/contracts/domain"
)

// commitImpactColumns are the columns of a change_impact row (ADR 0174), in the order of commitImpactArgs.
var commitImpactColumns = []string{"node_id", "version", "change_id", "impact_id", "key", "type", "intent", "rationale", "pre_version",
	"merged_with", "reason", "review", "effect", "log_seq", "produced_by", "execution", "via", "created_at"}

// commitImpactArgs are the arguments of a change_impact row but its time, which each repository encodes (the last
// column): the versions a row does not name are NULL, its review and effect JSON.
func commitImpactArgs(r CommitImpact) ([]any, error) {
	var review []byte
	if r.Review != nil {
		var err error
		if review, err = json.Marshal(r.Review); err != nil {
			return nil, err
		}
	}
	effect, err := json.Marshal(r.Effect)
	if err != nil {
		return nil, err
	}
	ver := func(v domain.Version) *int {
		if v == 0 {
			return nil
		}
		n := int(v)
		return &n
	}
	var rev any
	if review != nil {
		rev = string(review)
	}
	return []any{string(r.Node), int(r.Version), string(r.Change), nullUUID(string(r.Impact)), r.Key, r.Type, string(r.Intent), r.Rationale,
		ver(r.Pre), ver(r.MergedWith), r.Reason, rev, string(effect), r.LogSeq, r.ProducedBy, r.Execution, nullUUID(string(r.Via))}, nil
}

// commitImpactRow is the scan target of a change_impact row (sqlCommitImpacts), its time left to each repository.
type commitImpactRow struct {
	node, change, key, typ, intent, rationale, reason, producedBy, execution string
	impact, via, review                                                      *string
	version                                                                  int
	pre, mergedWith                                                          *int
	effect                                                                   []byte
	logSeq                                                                   int64
}

// dest are the scan destinations of a row, in the order of sqlCommitImpacts but its time.
func (r *commitImpactRow) dest() []any {
	return []any{&r.node, &r.version, &r.change, &r.impact, &r.key, &r.typ, &r.intent, &r.rationale, &r.pre, &r.mergedWith, &r.reason,
		&r.review, &r.effect, &r.logSeq, &r.producedBy, &r.execution, &r.via}
}

func (r *commitImpactRow) impactOf() (CommitImpact, error) {
	ci := CommitImpact{Node: domain.NodeID(r.node), Version: domain.Version(r.version), Change: domain.ChangeID(r.change), Key: r.key, Type: r.typ,
		Intent: domain.NodeIntent(r.intent), Rationale: r.rationale, Reason: r.reason, LogSeq: r.logSeq, ProducedBy: r.producedBy, Execution: r.execution}
	if r.impact != nil {
		ci.Impact = domain.ChangeImpactID(*r.impact)
	}
	if r.via != nil {
		ci.Via = domain.ChangeImpactID(*r.via)
	}
	if r.pre != nil {
		ci.Pre = domain.Version(*r.pre)
	}
	if r.mergedWith != nil {
		ci.MergedWith = domain.Version(*r.mergedWith)
	}
	if r.review != nil && *r.review != "" {
		ci.Review = &domain.Review{}
		if err := json.Unmarshal([]byte(*r.review), ci.Review); err != nil {
			return ci, err
		}
	}
	if len(r.effect) > 0 {
		if err := json.Unmarshal(r.effect, &ci.Effect); err != nil {
			return ci, err
		}
	}
	return ci, nil
}
