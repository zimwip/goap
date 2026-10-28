package domain

// Counters of a change: what it did to the graph, node by node, for statistics. The graph says what each version a
// change commits does to its node (EffectOf, recorded on the committed event and folded into ChangeImpact.Effect); the
// counters of the change are a fold of its impacts (StatsOf), net per node: a node created then edited by the change is
// one creation. The committed counters are kept on the header of the change (Change.Stats) at each commit and each merge
// of a sub-change, so a store reads them with no fold.

// Kinds of an ImpactEffect.
const (
	EffectCreated  = "created"
	EffectModified = "modified"
	EffectDeleted  = "deleted"
	EffectRestored = "restored"
)

// ImpactEffect is what the version a change committed for a node does to it, compared with the version the change
// started from (its impact's Pre; nothing for a creation): created, modified, deleted (the version is a tombstone) or
// restored (the version it started from was one), the properties set, changed or unset, whether its lifecycle state
// moved, and its outgoing links added, removed or changed (a link is its type and its target node, so another version of
// the same target is the same link). Requalified: a creation the change took as the modification of the node of its key
// the graph already held.
type ImpactEffect struct {
	Kind         string `json:"kind,omitempty"`
	Requalified  bool   `json:"requalified,omitempty"`
	Properties   int    `json:"props,omitempty"`
	State        bool   `json:"state,omitempty"`
	LinksAdded   int    `json:"linksAdded,omitempty"`
	LinksRemoved int    `json:"linksRemoved,omitempty"`
	LinksChanged int    `json:"linksChanged,omitempty"`
}

// IsZero reports an effect not recorded.
func (e ImpactEffect) IsZero() bool { return e == ImpactEffect{} }

// EffectOf is the effect of the version post with its outgoing links, over the version pre the change started from
// with its own (nil for a creation).
func EffectOf(pre *Node, preLinks []Link, post Node, postLinks []Link) ImpactEffect {
	var e ImpactEffect
	var left NodeShape
	switch {
	case pre == nil:
		e.Kind = EffectCreated
	case post.Deleted && !pre.Deleted:
		e.Kind = EffectDeleted
	case pre.Deleted && !post.Deleted:
		e.Kind = EffectRestored
	default:
		e.Kind = EffectModified
	}
	if pre != nil && !pre.Deleted {
		left = shapeOf(*pre, preLinks)
	} else if pre != nil {
		left.State = pre.State // a tombstone keeps its state: a restoration moves from it
	}
	right := NodeShape{}
	if !post.Deleted {
		right = shapeOf(post, postLinks)
	}
	for _, f := range DiffShapes(left, right) {
		switch f.Kind {
		case FieldProperty:
			if e.Kind != EffectDeleted {
				e.Properties++
			}
		case FieldState:
			e.State = e.Kind != EffectCreated && e.Kind != EffectDeleted && f.Old != "" // a move: a first state is none
		case FieldLink:
			switch f.Op {
			case OpAdded:
				e.LinksAdded++
			case OpRemoved:
				e.LinksRemoved++
			default:
				e.LinksChanged++
			}
		}
	}
	return e
}

// shapeOf is the shape a node and its outgoing links compare by: a link to another source is named by its URL.
func shapeOf(n Node, links []Link) NodeShape {
	s := NodeShape{Key: n.Key, Type: n.Type, State: n.State, Properties: n.Properties}
	for _, l := range links {
		to := l.To.ID
		if l.External() {
			to = NodeID(l.Target.String())
		}
		s.Links = append(s.Links, LinkShape{Type: l.Type, To: to, Properties: l.Properties})
	}
	return s
}

// ChangeStats are the counters of a change. The nodes and links are those its commits wrote (net per node: each impact
// counts once, by the effect of its last commit); Requalified counts, among the modified and restored nodes, the
// creations taken as modifications of existing nodes. Proposed, Accepted and Rejected count the impacts not committed
// yet, by review: they are read from the impacts, never stored (Committed drops them).
type ChangeStats struct {
	Created      int `json:"created,omitempty"`
	Modified     int `json:"modified,omitempty"`
	Deleted      int `json:"deleted,omitempty"`
	Restored     int `json:"restored,omitempty"`
	Requalified  int `json:"requalified,omitempty"`
	Properties   int `json:"props,omitempty"`
	States       int `json:"states,omitempty"`
	LinksAdded   int `json:"linksAdded,omitempty"`
	LinksRemoved int `json:"linksRemoved,omitempty"`
	LinksChanged int `json:"linksChanged,omitempty"`
	Proposed     int `json:"proposed,omitempty"`
	Accepted     int `json:"accepted,omitempty"`
	Rejected     int `json:"rejected,omitempty"`
}

// Nodes is the number of nodes the commits of the change wrote.
func (s ChangeStats) Nodes() int { return s.Created + s.Modified + s.Deleted + s.Restored }

// Pending is the number of impacts not committed yet.
func (s ChangeStats) Pending() int { return s.Proposed + s.Accepted + s.Rejected }

// Committed are the counters of what the change committed, the pending impacts left out: what its header keeps.
func (s ChangeStats) Committed() ChangeStats {
	s.Proposed, s.Accepted, s.Rejected = 0, 0, 0
	return s
}

// StatsOf folds the impacts of the main flow of a change (the candidates of an option and the superseded ones left
// out) into its counters. A committed impact whose effect was not recorded (a version a merge change wrote) counts by
// its intent.
func StatsOf(impacts Impacts) ChangeStats {
	var s ChangeStats
	for _, cn := range impacts.All() {
		if cn.Flow != "" || cn.Superseded {
			continue
		}
		if cn.Committed == nil {
			switch cn.Review {
			case ReviewAccepted:
				s.Accepted++
			case ReviewRejected:
				s.Rejected++
			default:
				s.Proposed++
			}
			continue
		}
		e := cn.Effect
		if e.Kind == "" {
			e.Kind = EffectModified
			if cn.Intent == IntentCreated {
				e.Kind = EffectCreated
			}
		}
		switch e.Kind {
		case EffectCreated:
			s.Created++
		case EffectDeleted:
			s.Deleted++
		case EffectRestored:
			s.Restored++
		default:
			s.Modified++
		}
		if e.Requalified {
			s.Requalified++
		}
		if e.State {
			s.States++
		}
		s.Properties += e.Properties
		s.LinksAdded += e.LinksAdded
		s.LinksRemoved += e.LinksRemoved
		s.LinksChanged += e.LinksChanged
	}
	return s
}
