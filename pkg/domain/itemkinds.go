package domain

import "sort"

// ItemKindInfo describes a kind of item for the people reading the catalogue (the "item types" of the web, ADR 0119).
type ItemKindInfo struct {
	Kind ItemKind `json:"kind"`
	// Origin is "core" for the kinds the graph knows itself, else the use case registering the kind (risk, assess…).
	Origin      string `json:"origin"`
	Description string `json:"description,omitempty"`
	// Permission is what writing an item of the kind asks of its writer (RequireItemPermission), if anything.
	Permission *ItemPermission `json:"permission,omitempty"`
}

// OriginCore is the origin of the kinds the graph knows itself.
const OriginCore = "core"

// coreKinds are the kinds ChangeItem.Validate knows without a registration.
var coreKinds = []ItemKindInfo{
	{Kind: KindArtifact, Description: "A product of an action (a document, a report, a plan…); its type names what it is."},
	{Kind: KindDecision, Description: "Accepts or rejects another item of the change."},
	{Kind: KindDecisionPoint, Description: "An event of a decision point (open, rule, answer, ratify), replayed into the decision points of the change (ADR 0009)."},
	{Kind: KindFlow, Description: "An event of the action flow: a step relaunched on a new flow, which is then adopted or discarded."},
	{Kind: KindReview, Description: "The record of a review of the change impacts; every verdict is made by a submitted review (ADR 0080, 0105)."},
	{Kind: KindSignal, Description: "A named notification other agents or a live parent may react to; its type is the signal's name."},
	{Kind: KindTransition, Description: "A move of the lifecycle of the change, written only by a transition (ADR 0058)."},
}

// DescribeItemKind tells the catalogue (ItemKinds) which use case a registered kind comes from and what it is. Like
// RegisterItemKind, it is explicit, idempotent and safe for concurrent use.
func DescribeItemKind(kind ItemKind, origin, description string) {
	itemKinds.mu.Lock()
	defer itemKinds.mu.Unlock()
	if itemKinds.desc == nil {
		itemKinds.desc = map[ItemKind]ItemKindInfo{}
	}
	itemKinds.desc[kind] = ItemKindInfo{Kind: kind, Origin: origin, Description: description}
}

// ItemKinds lists the kinds of item this process accepts: the core kinds and the registered ones, by kind.
func ItemKinds() []ItemKindInfo {
	itemKinds.mu.RLock()
	defer itemKinds.mu.RUnlock()
	out := make([]ItemKindInfo, 0, len(coreKinds)+len(itemKinds.m))
	for _, k := range coreKinds {
		k.Origin = OriginCore
		out = append(out, k)
	}
	for kind := range itemKinds.m {
		info, ok := itemKinds.desc[kind]
		if !ok {
			info = ItemKindInfo{Kind: kind}
		}
		out = append(out, info)
	}
	for i := range out {
		if p, ok := itemKinds.perms[out[i].Kind]; ok {
			out[i].Permission = &p
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}
