package domain

import (
	"encoding/json"
	"slices"
	"sort"
)

// Rebasing a sub-change onto its parent (ADR 0082): a draft of the sub-change is merged three-way with the parent's draft
// of the node, against the state the sub-change started from. A field changed on both sides to different values is a
// conflict, named; the sub-change settles it by editing its draft.

// Conflict names (ADR 0082 §1).
const (
	ConflictOwner = "owner"
	ConflictState = "state"
	// ConflictNode is a conflict on the whole node: the parent rejected or withdrew its impact of it.
	ConflictNode = "node"
)

// ConflictProp names a conflict on a property.
func ConflictProp(key string) string { return "props." + key }

// ConflictLink names a conflict on the outgoing link of a type to a node.
func ConflictLink(typ string, to NodeID) string { return "link:" + typ + ":" + string(to) }

// MergeDrafts merges ours (the sub-change's draft) with theirs (the parent's) against base (what ours started from), field by
// field: each property (set or unset), the owner, the state and each outgoing link (identified by its type and target
// node; its target version and properties are its value). A field changed on one side takes that side's value, on both
// sides to one value that value; changed on both sides differently, it keeps ours and is named in the conflicts (sorted).
// The result is ours otherwise (key, type, origins, base, links' ids); a link brought by theirs has no id (the caller gives
// it one).
func MergeDrafts(base, ours, theirs Draft) (Draft, []string) {
	out := ours.Clone()
	var conflicts []string
	keys := map[string]bool{}
	for _, m := range []map[string]any{base.Properties, ours.Properties, theirs.Properties} {
		for k := range m {
			keys[k] = true
		}
	}
	for _, k := range sortedKeys(keys) {
		b, bok := base.Properties[k]
		o, ook := ours.Properties[k]
		t, tok := theirs.Properties[k]
		switch {
		case sameValue(o, ook, b, bok):
			if out.Properties == nil {
				out.Properties = map[string]any{}
			}
			if tok {
				out.Properties[k] = t
			} else {
				delete(out.Properties, k)
			}
		case sameValue(t, tok, b, bok) || sameValue(o, ook, t, tok):
		default:
			conflicts = append(conflicts, ConflictProp(k))
		}
	}
	var conflict bool
	if out.Owner, conflict = merge3(base.Owner, ours.Owner, theirs.Owner); conflict {
		conflicts = append(conflicts, ConflictOwner)
	}
	if out.State, conflict = merge3(base.State, ours.State, theirs.State); conflict {
		conflicts = append(conflicts, ConflictState)
	}
	links, lc := mergeLinks(base.Links, ours.Links, theirs.Links)
	out.Links = links
	conflicts = append(conflicts, lc...)
	sort.Strings(conflicts)
	return out, conflicts
}

func merge3[T comparable](base, ours, theirs T) (T, bool) {
	switch {
	case ours == base:
		return theirs, false
	case theirs == base || ours == theirs:
		return ours, false
	}
	return ours, true
}

func linkKey(l DraftLink) string { return ConflictLink(l.Type, l.To.ID) }

func linksByKey(ls []DraftLink) map[string]DraftLink {
	out := map[string]DraftLink{}
	for _, l := range ls {
		if _, ok := out[linkKey(l)]; !ok {
			out[linkKey(l)] = l
		}
	}
	return out
}

func sameLink(a DraftLink, aok bool, b DraftLink, bok bool) bool {
	if aok != bok {
		return false
	}
	return !aok || (a.To.Version == b.To.Version && sameValue(a.Properties, true, b.Properties, true))
}

// mergeLinks merges the outgoing links: ours in their order (the ones theirs changed or removed updated), then the ones
// theirs added, by key.
func mergeLinks(base, ours, theirs []DraftLink) ([]DraftLink, []string) {
	bm, om, tm := linksByKey(base), linksByKey(ours), linksByKey(theirs)
	var conflicts []string
	take := map[string]*DraftLink{} // the outcome of a key ours did not change: theirs (nil: removed)
	keys := map[string]bool{}
	for _, m := range []map[string]DraftLink{bm, om, tm} {
		for k := range m {
			keys[k] = true
		}
	}
	for _, k := range sortedKeys(keys) {
		b, bok := bm[k]
		o, ook := om[k]
		t, tok := tm[k]
		switch {
		case sameLink(o, ook, b, bok):
			if tok {
				t := t
				take[k] = &t
			} else {
				take[k] = nil
			}
		case sameLink(t, tok, b, bok) || sameLink(o, ook, t, tok):
		default:
			conflicts = append(conflicts, k)
		}
	}
	var out []DraftLink
	seen := map[string]bool{}
	for _, l := range ours {
		k := linkKey(l)
		if seen[k] {
			out = append(out, l) // a second link of the same type to the same node: kept as it is
			continue
		}
		seen[k] = true
		t, decided := take[k]
		switch {
		case !decided:
			out = append(out, l)
		case t != nil:
			l.To, l.Properties = t.To, cloneProps(t.Properties)
			out = append(out, l)
		}
	}
	var added []string
	for k, t := range take {
		if t != nil && !seen[k] {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	for _, k := range added {
		t := *take[k]
		out = append(out, DraftLink{Type: t.Type, To: t.To, Properties: cloneProps(t.Properties)})
	}
	return out, conflicts
}

// sameValue compares two values as JSON (the log stores them so: a number may come back as another Go type).
func sameValue(a any, aok bool, b any, bok bool) bool {
	if aok != bok {
		return false
	}
	if !aok {
		return true
	}
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PendingConflicts are the conflicts a change impact's last rebase named on the main flow (ADR 0082 §2), less the ones an edit
// of its draft settled since: a property set or unset settles its conflict, an owner edit the owner, a transition the
// state, a link added, removed or updated the link of its type to its target, any edit the node; an `updated` event with
// `resolved` settles all. A new draft (a checkout, an install that is not a rebase) or the end of the draft clears them.
func PendingConflicts(events []ImpactEvent, impact ChangeImpactID) []string {
	var pending []string
	settle := func(name string) {
		pending = slices.DeleteFunc(pending, func(c string) bool { return c == name })
	}
	for _, e := range events {
		if e.Impact != impact || e.Flow != "" {
			continue
		}
		switch e.Op {
		case ImpactCreated, ImpactCheckedOut, ImpactCancelled, ImpactWithdrawn, ImpactLanded, ImpactIntegrated:
			pending = nil
		case ImpactTransitioned:
			if r, ok := e.Patch["rebased"]; ok && r != nil {
				pending = stringsOf(e.Patch["conflicts"])
				continue
			}
			if e.Draft != nil {
				pending = nil // a draft installed wholesale (an adoption, an integration)
				continue
			}
			if e.Patch["state"] != nil {
				settle(ConflictState)
			}
			p := patchOf(e.Patch)
			for k := range p.Props {
				settle(ConflictProp(k))
			}
		case ImpactUpdated:
			if done, _ := e.Patch["resolved"].(bool); done {
				pending = nil
				continue
			}
			settle(ConflictNode)
			p := patchOf(e.Patch)
			for k := range p.Props {
				settle(ConflictProp(k))
			}
			for _, k := range p.Unset {
				settle(ConflictProp(k))
			}
			if p.OwnerID != "" {
				settle(ConflictOwner)
			}
			for _, name := range []string{"addLink", "removeLink", "updateLink"} {
				l, _ := e.Patch[name].(map[string]any)
				typ, _ := l["type"].(string)
				to, _ := l["toId"].(string)
				if typ != "" && to != "" {
					settle(ConflictLink(typ, NodeID(to)))
				}
			}
		}
	}
	return pending
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return slices.Clone(x)
	case []any:
		var out []string
		for _, s := range x {
			if s, ok := s.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
