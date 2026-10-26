package graphsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/rpcerr"
)

// Staged settings (ADR 0037 §3, ADR 0122). What the settings dialog edits is graph data staged in the personal changes
// of the caller, one per namespace: an edit checks the node out and writes its draft, a removal is the transition that
// retires it, Save accepts and applies, Discard purges. The dialog used to drive each step itself, several calls per
// edit; these operations do the same steps here, with the same gates as the RPCs they replace (the creation of a node
// is authorized like ImpactNodeCreate, every node of an adminOnly type needs the access permission), and answer the
// whole pending state so the dialog needs no read after a write.

// stateActive is the state a restored node goes back to (lifecycle config, ADR 0076).
const stateActive = "active"

// settingsLocks serializes the settings operations of a subject in this process: an edit reads the pending state
// and builds on it, as the dialog did one edit after the other.
var settingsLocks sync.Map

func lockSettings(subject string) func() {
	mu, _ := settingsLocks.LoadOrStore(subject, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// settingsCaller is the subject of a settings call: a person, never anonymous.
func settingsCaller(ctx context.Context) (string, error) {
	me := subjectOf(ctx)
	if me == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("settings need an identified caller"))
	}
	return me, nil
}

// settingsChanges are the open personal changes of the subject, by namespace (the first one of a namespace wins).
func (h *Handler) settingsChanges(ctx context.Context, subject string) (map[string]domain.Change, error) {
	cs, err := h.Graph.ListChanges(ctx, graph.ChangesFilter{OwnerOrg: access.PersonalUnit(subject), Status: []domain.ChangeStatus{domain.ChangeDraft, domain.ChangeActive}})
	if err != nil {
		return nil, err
	}
	out := map[string]domain.Change{}
	for _, c := range cs {
		if c.Namespace == "" || c.ParentID != "" || !access.IsPersonalTo(c, subject) {
			continue
		}
		if _, ok := out[c.Namespace]; !ok {
			out[c.Namespace] = c
		}
	}
	return out, nil
}

// settingsImpacts are the impacts of a personal change the dialog shows: those of the main flow.
func settingsImpacts(c domain.Change) []domain.ChangeImpact {
	var out []domain.ChangeImpact
	for _, cn := range c.Nodes {
		if cn.Flow == "" && !cn.Superseded && cn.Review != domain.ReviewRejected {
			out = append(out, cn)
		}
	}
	return out
}

func impactOfKey(c domain.Change, key string) *domain.ChangeImpact {
	for _, cn := range settingsImpacts(c) {
		if cn.Key == key {
			return &cn
		}
	}
	return nil
}

// pendingSettings reads the pending state of the subject.
func (h *Handler) pendingSettings(ctx context.Context, subject string) (*graphv1.PendingSettingsResponse, error) {
	byNs, err := h.settingsChanges(ctx, subject)
	if err != nil {
		return nil, err
	}
	out := &graphv1.PendingSettingsResponse{}
	for _, ns := range sortedKeys(byNs) {
		c, err := h.Graph.Change(ctx, byNs[ns].ID)
		if err != nil {
			return nil, err
		}
		p := &graphv1.PendingSettings{Namespace: ns, ChangeId: string(c.ID)}
		for _, cn := range settingsImpacts(c) {
			s := &graphv1.StagedSetting{Key: cn.Key, Type: cn.Type, Created: cn.Intent == domain.IntentCreated, ChangeImpactId: string(cn.ID), Written: cn.Drafted()}
			ref := cn.Post
			if ref == nil {
				ref = cn.Pre
			}
			if ref != nil {
				// a draft is read through the change (it has no version, ADR 0079)
				if v, err := h.Graph.ChangeNodeView(ctx, c.ID, "", *ref); err == nil {
					s.Retire = v.State == access.StateRetired
					if s.Key == "" {
						s.Key = v.Key
					}
					if s.Type == "" {
						s.Type = v.Type
					}
					if cn.Drafted() {
						s.Props = pbconv.Struct(v.Properties)
					}
				} else if !errors.Is(err, graph.ErrNotFound) {
					return nil, err
				}
			}
			if s.Key == "" || s.Type == "" {
				continue
			}
			p.Nodes = append(p.Nodes, s)
		}
		out.Changes = append(out.Changes, p)
	}
	return out, nil
}

// settingsChange is the personal change of the subject for a namespace, opened on main's head when there is none (the
// empty baseline for a namespace nothing was written in yet, ADR 0056).
func (h *Handler) settingsChange(ctx context.Context, byNs map[string]domain.Change, ns string) (domain.Change, error) {
	if c, ok := byNs[ns]; ok {
		return h.Graph.Change(ctx, c.ID)
	}
	head, err := h.Graph.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return domain.Change{}, err
	}
	owner, err := h.resolveOwner(ctx, OwnerMe)
	if err != nil {
		return domain.Change{}, err
	}
	c, err := h.Graph.CreateChange(ctx, graph.NewChange{Title: fmt.Sprintf("My %s settings", ns), Intent: fmt.Sprintf("Change the %s settings", ns), Namespace: ns,
		BaselineID: head.ID, OwnBranch: true, OwnerOrg: owner, ProjectID: h.projectOf(ctx, "")})
	if err != nil {
		return domain.Change{}, err
	}
	byNs[ns] = c
	return c, nil
}

// dropIfEmpty purges a personal change left with no impact: it has no reason to stay.
func (h *Handler) dropIfEmpty(ctx context.Context, byNs map[string]domain.Change, ns string) error {
	c, ok := byNs[ns]
	if !ok {
		return nil
	}
	c, err := h.Graph.Change(ctx, c.ID)
	if err != nil {
		return err
	}
	if len(settingsImpacts(c)) > 0 {
		return nil
	}
	if _, err := h.Graph.PurgeChange(ctx, c.ID); err != nil {
		return err
	}
	delete(byNs, ns)
	return nil
}

// stageSetting stages one edit.
func (h *Handler) stageSetting(ctx context.Context, byNs map[string]domain.Change, e *graphv1.SettingEdit) error {
	if e.Namespace == "" || e.Key == "" || e.Type == "" {
		return fmt.Errorf("a setting names its namespace, type and key: %w", graph.ErrInvalid)
	}
	_, had := byNs[e.Namespace]
	c, err := h.settingsChange(ctx, byNs, e.Namespace)
	if err != nil {
		return err
	}
	by := authz.From(ctx).Subject
	cn := impactOfKey(c, e.Key)
	// the node as the change sees it: its draft, else the version the change was forked from
	view, err := h.Graph.ChangeNodeViewByKey(ctx, c.ID, "", e.Namespace, e.Key)
	exists := err == nil && !view.Deleted
	if err != nil && !errors.Is(err, graph.ErrNotFound) {
		return err
	}
	if exists && view.Type != e.Type {
		return fmt.Errorf("%s is a %s, not a %s: %w", e.Key, view.Type, e.Type, graph.ErrInvalid)
	}
	if err := h.gateAccess(ctx, e.Type); err != nil {
		return err
	}
	drafted := cn != nil && cn.Drafted()
	checkout := graph.NodeCheckout{Key: e.Key, ProducedBy: by}
	if cn != nil {
		checkout = graph.NodeCheckout{Impact: cn.ID, ProducedBy: by}
	}

	if e.Retire {
		switch {
		case cn != nil && cn.Intent == domain.IntentCreated:
			// a node created by the pending change is simply given up
			if err := h.Graph.WithdrawImpact(ctx, c.ID, cn.ID, "", ""); err != nil {
				return err
			}
			return h.dropIfEmpty(ctx, byNs, e.Namespace)
		case !exists:
			// a node that exists nowhere has nothing to retire
			if !had {
				return h.dropIfEmpty(ctx, byNs, e.Namespace)
			}
			return nil
		case view.State == access.StateRetired:
			return nil
		}
		if drafted {
			// the edits staged on the node go with it: the retirement starts from the version the node has
			if _, err := h.Graph.ImpactNodeCancel(ctx, c.ID, cn.ID, "", ""); err != nil {
				return err
			}
		}
		checkout.Rationale = "Retire " + e.Key
		_, err := h.Graph.ImpactNodeTransition(ctx, c.ID, graph.NodeTransition{NodeCheckout: checkout, To: access.StateRetired})
		return err
	}

	props := pbconv.Map(e.Props)
	if !exists {
		if err := h.checkCreate(ctx, c, e.Type); err != nil {
			return err
		}
		_, err := h.Graph.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Key: e.Key, Type: e.Type, Properties: props, Rationale: "Create " + e.Key, ProducedBy: by})
		return err
	}
	if view.State == access.StateRetired {
		// a retired node is restored first: a transition of its own, then it is edited
		checkout.Rationale = "Restore " + e.Key
		out, err := h.Graph.ImpactNodeTransition(ctx, c.ID, graph.NodeTransition{NodeCheckout: checkout, To: stateActive})
		if err != nil {
			return err
		}
		checkout, drafted = graph.NodeCheckout{Impact: out.ID, ProducedBy: by}, true
	}
	if !drafted {
		checkout.Rationale = "Change " + e.Key
		out, err := h.Graph.ImpactNodeCheckout(ctx, c.ID, checkout)
		if err != nil {
			return err
		}
		checkout.Impact = out.ID
	}
	if len(props) == 0 {
		return nil
	}
	_, err = h.Graph.ImpactNodeUpdate(ctx, c.ID, checkout.Impact, graph.NodeUpdate{Properties: props})
	return err
}

// GetPendingSettings answers the edits of the settings dialog the caller has not saved yet (an earlier session may have
// left some).
func (h *Handler) GetPendingSettings(ctx context.Context, r *connect.Request[graphv1.GetPendingSettingsRequest]) (*connect.Response[graphv1.PendingSettingsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	me, err := settingsCaller(ctx)
	if err != nil {
		return nil, err
	}
	defer lockSettings(me)()
	return res(h.pendingSettings(ctx, me))
}

// StageSettings stages edits in order; the first refused one stops the rest (those before it stay staged).
func (h *Handler) StageSettings(ctx context.Context, r *connect.Request[graphv1.StageSettingsRequest]) (*connect.Response[graphv1.PendingSettingsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	me, err := settingsCaller(ctx)
	if err != nil {
		return nil, err
	}
	defer lockSettings(me)()
	byNs, err := h.settingsChanges(ctx, me)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	for _, e := range r.Msg.Edits {
		if err := h.stageSetting(ctx, byNs, e); err != nil {
			return nil, rpcerr.ToConnect(err)
		}
	}
	return res(h.pendingSettings(ctx, me))
}

// SaveSettings accepts the staged edits that modify something, in one review per change, and applies the changes; an
// edit never written or put back as it was is withdrawn (only a modification is accepted, ADR 0102), and a change left
// with nothing is removed.
func (h *Handler) SaveSettings(ctx context.Context, r *connect.Request[graphv1.SaveSettingsRequest]) (*connect.Response[graphv1.PendingSettingsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	me, err := settingsCaller(ctx)
	if err != nil {
		return nil, err
	}
	defer lockSettings(me)()
	byNs, err := h.settingsChanges(ctx, me)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	for _, ns := range sortedKeys(byNs) {
		if err := h.saveSettings(ctx, byNs[ns].ID, me); err != nil {
			return nil, rpcerr.ToConnect(err)
		}
	}
	return res(h.pendingSettings(ctx, me))
}

func (h *Handler) saveSettings(ctx context.Context, id domain.ChangeID, me string) error {
	c, err := h.Graph.ChangeChecked(ctx, id)
	if err != nil {
		return err
	}
	var drop []domain.ChangeImpactID
	b := domain.ReviewBatch{By: me}
	for _, cn := range settingsImpacts(c) {
		if !cn.Drafted() || (cn.Validation != nil && !cn.Validation.Modified) {
			drop = append(drop, cn.ID)
			continue
		}
		// Save is an explicit act of the owner on a personal change (ADR 0037): they review their own staged edits here,
		// it is not a side effect of editing (an edit never reviews, ADR 0079). The acceptance runs the validations on
		// the draft; applying the change writes the versions.
		b.Verdicts = append(b.Verdicts, domain.ImpactVerdict{Impact: cn.ID, Status: domain.ReviewAccepted, Comment: "Saved by its owner"})
	}
	if len(drop) > 0 {
		if err := h.Graph.WithdrawImpacts(ctx, id, drop, "", ""); err != nil {
			return err
		}
	}
	if len(b.Verdicts) == 0 {
		// nothing left to save: the change goes, it committed nothing
		_, err := h.Graph.PurgeChange(ctx, id)
		return err
	}
	if _, err := h.Graph.ImpactNodeReviewBatch(ctx, id, b); err != nil {
		return err
	}
	if _, err := h.Graph.CommitChange(ctx, id, ""); err != nil {
		return err
	}
	_, err = h.Graph.CloseChange(ctx, id)
	return err
}

// DiscardSettings removes the personal changes holding the staged edits.
func (h *Handler) DiscardSettings(ctx context.Context, r *connect.Request[graphv1.DiscardSettingsRequest]) (*connect.Response[graphv1.PendingSettingsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	me, err := settingsCaller(ctx)
	if err != nil {
		return nil, err
	}
	defer lockSettings(me)()
	byNs, err := h.settingsChanges(ctx, me)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	for _, ns := range sortedKeys(byNs) {
		if _, err := h.Graph.PurgeChange(ctx, byNs[ns].ID); err != nil {
			return nil, rpcerr.ToConnect(err)
		}
	}
	return res(h.pendingSettings(ctx, me))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
