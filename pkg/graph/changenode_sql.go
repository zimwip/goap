package graph

import (
	"encoding/json"

	"github.com/zimwip/goap/pkg/domain"
)

// cnRow is the flat storage form of a change node shared by the SQL backends.
type cnRow struct {
	ID                          string
	NodeID                      *string
	Pre, Post, Landed           *int
	Reviews, DerivedFrom, Items []byte
	Key, Type, Intent, Review   string
	Rationale, Via              string
	Recheck                     bool
	ProducedBy, Execution, Flow string
	Superseded                  bool
}

func toCNRow(cn domain.ChangeNode) (cnRow, error) {
	r := cnRow{ID: string(cn.ID), Key: cn.Key, Type: cn.Type, Intent: string(cn.Intent), Review: string(cn.Review),
		Rationale: cn.Rationale, Via: string(cn.Via), Recheck: cn.Recheck, ProducedBy: cn.ProducedBy, Execution: cn.Execution, Flow: cn.Flow, Superseded: cn.Superseded}
	ver := func(ref *domain.NodeRef) *int {
		if ref == nil {
			return nil
		}
		v := int(ref.Version)
		return &v
	}
	r.Pre, r.Post, r.Landed = ver(cn.Pre), ver(cn.Post), ver(cn.Landed)
	switch {
	case cn.Pre != nil:
		s := string(cn.Pre.ID)
		r.NodeID = &s
	case cn.Post != nil:
		s := string(cn.Post.ID)
		r.NodeID = &s
	}
	var err error
	if r.Reviews, err = json.Marshal(nonNil(cn.Reviews)); err != nil {
		return r, err
	}
	if r.DerivedFrom, err = json.Marshal(nonNil(cn.DerivedFrom)); err != nil {
		return r, err
	}
	r.Items, err = json.Marshal(nonNil(cn.Items))
	return r, err
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (r cnRow) node() (domain.ChangeNode, error) {
	cn := domain.ChangeNode{ID: domain.ChangeNodeID(r.ID), Key: r.Key, Type: r.Type, Intent: domain.NodeIntent(r.Intent),
		Review: domain.NodeReview(r.Review), Rationale: r.Rationale, Via: domain.ChangeNodeID(r.Via), Recheck: r.Recheck,
		ProducedBy: r.ProducedBy, Execution: r.Execution, Flow: r.Flow, Superseded: r.Superseded}
	ref := func(v *int) *domain.NodeRef {
		if v == nil || r.NodeID == nil {
			return nil
		}
		return &domain.NodeRef{ID: domain.NodeID(*r.NodeID), Version: domain.Version(*v)}
	}
	cn.Pre, cn.Post, cn.Landed = ref(r.Pre), ref(r.Post), ref(r.Landed)
	if err := json.Unmarshal(r.Reviews, &cn.Reviews); err != nil {
		return cn, err
	}
	if len(cn.Reviews) == 0 {
		cn.Reviews = nil
	}
	if err := json.Unmarshal(r.DerivedFrom, &cn.DerivedFrom); err != nil {
		return cn, err
	}
	if len(cn.DerivedFrom) == 0 {
		cn.DerivedFrom = nil
	}
	if err := json.Unmarshal(r.Items, &cn.Items); err != nil {
		return cn, err
	}
	if len(cn.Items) == 0 {
		cn.Items = nil
	}
	return cn, nil
}
