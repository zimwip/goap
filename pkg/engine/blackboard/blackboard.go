// Package blackboard is the execution view of a change (ADR 0098): the change, read and written with the semantics of
// the methodologies it carries. It stores nothing of its own: what the engine adds to a change are change objects of
// the built-in domain execution (execution@Methodology, execution@Run, ...), which the change validates by their type
// without knowing what they mean. The view is read from a blackboard of the graph (its FacetObjects) and the writes are
// domain.ObjectWrite values the engine submits.
package blackboard

import (
	"cmp"
	"slices"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// The change object types of the engine (the built-in domain execution).
const (
	TypeMethodology = "execution@Methodology"
	TypeState       = "execution@State"
	TypeTransition  = "execution@Transition"
	TypeFact        = "execution@Fact"
	TypeOption      = "execution@Option"
	TypeRun         = "execution@Run"
	TypeJournal     = "execution@Journal"
	TypeModelCall   = "execution@ModelCall"
)

// Roles of a methodology on a change.
const (
	RolePrimary   = "primary"
	RoleCompanion = "companion"
)

// MethodologyRef is a methodology a change carries (execution@Methodology): its version, its role (the primary one, or
// a transverse companion, ADR 0036) and the goal the change pursues with it.
type MethodologyRef struct {
	Name    string
	Version string
	Role    string
	Goal    string
}

// RunRef is a run of the engine working on a change (execution@Run): a pointer to the run the engine stores.
type RunRef struct {
	ID          string
	Methodology string
	Agent       string
	Goal        string
	Status      string
	StartedAt   time.Time
}

// View is the execution view of a change: its change objects read with their meaning.
type View struct {
	Change  domain.Change
	Objects []domain.ChangeObject
}

// Of is the view of a blackboard of the graph.
func Of(bb domain.Blackboard) View {
	return View{Change: bb.Change, Objects: domain.ObjectsOf(bb)}
}

// OfType lists the change objects of a type.
func (v View) OfType(typ string) []domain.ChangeObject {
	var out []domain.ChangeObject
	for _, o := range v.Objects {
		if o.Type == typ {
			out = append(out, o)
		}
	}
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// Methodologies lists the methodologies the change carries, the primary one first.
func (v View) Methodologies() []MethodologyRef {
	var primary, others []MethodologyRef
	for _, o := range v.OfType(TypeMethodology) {
		r := MethodologyRef{Name: o.Key, Version: str(o.Value, "version"), Role: str(o.Value, "role"), Goal: str(o.Value, "goal")}
		if r.Role == RolePrimary {
			primary = append(primary, r)
		} else {
			others = append(others, r)
		}
	}
	return append(primary, others...)
}

// Methodology returns the methodology the change carries under a name.
func (v View) Methodology(name string) (MethodologyRef, bool) {
	for _, r := range v.Methodologies() {
		if r.Name == name {
			return r, true
		}
	}
	return MethodologyRef{}, false
}

// Runs lists the runs working on the change, in the order they started.
func (v View) Runs() []RunRef {
	var out []RunRef
	for _, o := range v.OfType(TypeRun) {
		r := RunRef{ID: o.Key, Methodology: str(o.Value, "methodology"), Agent: str(o.Value, "agent"), Goal: str(o.Value, "goal"), Status: str(o.Value, "status")}
		if t, err := time.Parse(time.RFC3339, str(o.Value, "startedAt")); err == nil {
			r.StartedAt = t
		}
		out = append(out, r)
	}
	return out
}

// DeclareMethodology is the write of a methodology the change carries.
func DeclareMethodology(r MethodologyRef) domain.ObjectWrite {
	if r.Role == "" {
		r.Role = RolePrimary
	}
	value := map[string]any{"name": r.Name, "role": r.Role}
	if r.Version != "" {
		value["version"] = r.Version
	}
	if r.Goal != "" {
		value["goal"] = r.Goal
	}
	return domain.ObjectWrite{Type: TypeMethodology, Value: value}
}

// RecordRun is the write of a run working on the change: its key is the run, its labels name it as the process.
func RecordRun(r RunRef) domain.ObjectWrite {
	value := map[string]any{"methodology": r.Methodology, "agent": r.Agent, "status": r.Status}
	if r.Goal != "" {
		value["goal"] = r.Goal
	}
	if !r.StartedAt.IsZero() {
		value["startedAt"] = r.StartedAt.UTC().Format(time.RFC3339)
	}
	return domain.ObjectWrite{Type: TypeRun, Key: r.ID, Value: value, Labels: map[string]string{"process": r.ID}}
}

// StateRef is the state of the change in the lifecycle of its methodology (execution@State, ADR 0058): the
// methodology defines the lifecycle, the engine moves the state, the change knows neither.
type StateRef struct {
	Lifecycle string
	State     string
	// Version is the version of the change object: a move writes over it (domain.ObjectWrite.Expect).
	Version int
}

// State is the state of the change; ok false while nothing recorded one (the change is then in the initial state of
// its lifecycle).
func (v View) State() (StateRef, bool) {
	for _, o := range v.OfType(TypeState) {
		if o.Workspace == "" {
			return StateRef{Lifecycle: str(o.Value, "lifecycle"), State: str(o.Value, "state"), Version: o.Version}, true
		}
	}
	return StateRef{}, false
}

// Move is a move of the lifecycle of the change (execution@Transition), at its position in the log of the change.
type Move struct {
	Key        string
	Transition string
	From, To   string
	// Decision is the decision point that gated the move, when there was one: a point gates one move only.
	Decision string
	By       string
	Seq      int64
}

// Moves are the moves of the lifecycle of the change, in the order they were made.
func (v View) Moves() []Move {
	var out []Move
	for _, o := range v.OfType(TypeTransition) {
		out = append(out, Move{Key: o.Key, Transition: str(o.Value, "transition"), From: str(o.Value, "from"), To: str(o.Value, "to"),
			Decision: str(o.Value, "decision"), By: str(o.Value, "by"), Seq: o.Seq})
	}
	slices.SortFunc(out, func(a, b Move) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// SetState is the write of the state of the change, over the version the writer read (StateRef.Version, 0: none).
func SetState(lifecycle, state string, read int) domain.ObjectWrite {
	return domain.ObjectWrite{Type: TypeState, Value: map[string]any{"lifecycle": lifecycle, "state": state}, Expect: &read}
}

// RecordMove is the write of a move of the lifecycle; unmet and reserve say which objectives it goes with reserve of
// and the derogations that covered them (ADR 0075 §3).
func RecordMove(m Move, unmet, reserve any) domain.ObjectWrite {
	value := map[string]any{"transition": m.Transition, "from": m.From, "to": m.To}
	if m.Decision != "" {
		value["decision"] = m.Decision
	}
	if m.By != "" {
		value["by"] = m.By
	}
	if unmet != nil {
		value["unmet"], value["reserve"] = unmet, reserve
	}
	return domain.ObjectWrite{Type: TypeTransition, Value: value}
}
