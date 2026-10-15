package domain

import "sort"

// ItemTypeInfo describes a kind of item for the people reading the catalogue (the "item types" of the web, ADR 0119).
type ItemTypeInfo struct {
	Type ItemType `json:"type"`
	// Origin is "core" for the kinds the graph knows itself, else the use case registering the kind (risk, assess…).
	Origin      string `json:"origin"`
	Description string `json:"description,omitempty"`
	// Permission is what writing an item of the kind asks of its writer (RequireItemPermission), if anything.
	Permission *ItemPermission `json:"permission,omitempty"`
}

// OriginCore is the origin of the kinds the graph knows itself.
const OriginCore = "core"

// coreTypes are the kinds ChangeItem.Validate knows without a registration.
var coreTypes = []ItemTypeInfo{
	{Type: TypeArtifact, Description: "A product of an action (a document, a report, a plan…); its type names what it is."},
	{Type: TypeDecision, Description: "Accepts or rejects another item of the change."},
	{Type: TypeDecisionPoint, Description: "An event of a decision point (open, rule, answer, ratify), replayed into the decision points of the change (ADR 0009)."},
	{Type: TypeFlow, Description: "An event of the action flow: a step relaunched on a new flow, which is then adopted or discarded."},
	{Type: TypeReview, Description: "The record of a review of the change impacts; every verdict is made by a submitted review (ADR 0080, 0105)."},
	{Type: TypeSignal, Description: "A named notification other agents or a live parent may react to; its type is the signal's name."},
	{Type: TypeTransition, Description: "A move of the lifecycle of the change, written only by a transition (ADR 0058)."},
}

// DescribeItemType tells the catalogue (ItemKinds) which use case a registered kind comes from and what it is. Like
// RegisterItemKind, it is explicit, idempotent and safe for concurrent use.
func DescribeItemType(kind ItemType, origin, description string) {
	itemTypes.mu.Lock()
	defer itemTypes.mu.Unlock()
	if itemTypes.desc == nil {
		itemTypes.desc = map[ItemType]ItemTypeInfo{}
	}
	itemTypes.desc[kind] = ItemTypeInfo{Type: kind, Origin: origin, Description: description}
}

// ItemTypes lists the kinds of item this process accepts: the core kinds and the registered ones, by kind.
func ItemTypes() []ItemTypeInfo {
	itemTypes.mu.RLock()
	defer itemTypes.mu.RUnlock()
	out := make([]ItemTypeInfo, 0, len(coreTypes)+len(itemTypes.m))
	for _, k := range coreTypes {
		k.Origin = OriginCore
		out = append(out, k)
	}
	for kind := range itemTypes.m {
		info, ok := itemTypes.desc[kind]
		if !ok {
			info = ItemTypeInfo{Type: kind}
		}
		out = append(out, info)
	}
	for i := range out {
		if p, ok := itemTypes.perms[out[i].Type]; ok {
			out[i].Permission = &p
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}
