package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

// LogEntryToPB converts an entry of the log of a change (ADR 0030).
func LogEntryToPB(e domain.LogEntry) *graphv1.LogEntry {
	return &graphv1.LogEntry{Seq: e.Seq, Id: e.ID, ChangeId: string(e.Change), Type: e.Type, Flow: e.Flow, ProcessId: e.Process,
		Execution: e.Execution, Subject: e.Subject, By: e.By, At: Time(e.At), Payload: string(e.Payload), Labels: e.Labels}
}

// LogEntryFromPB converts a protobuf entry of the log of a change.
func LogEntryFromPB(e *graphv1.LogEntry) domain.LogEntry {
	return domain.LogEntry{Seq: e.Seq, ID: e.Id, Change: domain.ChangeID(e.ChangeId), Type: e.Type, Flow: e.Flow, Process: e.ProcessId,
		Execution: e.Execution, Subject: e.Subject, By: e.By, At: FromTime(e.At), Payload: []byte(e.Payload), Labels: labelsOrNil(e.Labels)}
}

// LogEntriesFromPB converts protobuf entries.
func LogEntriesFromPB(es []*graphv1.LogEntry) []domain.LogEntry {
	out := make([]domain.LogEntry, len(es))
	for i, e := range es {
		out[i] = LogEntryFromPB(e)
	}
	return out
}

func labelsOrNil(l map[string]string) map[string]string {
	if len(l) == 0 {
		return nil
	}
	return l
}

// ChangeObjectToPB converts a version of a change object (ADR 0098).
func ChangeObjectToPB(o domain.ChangeObject) *graphv1.ChangeObject {
	return &graphv1.ChangeObject{ChangeId: string(o.Change), Type: o.Type, Key: o.Key, Workspace: o.Workspace, Version: int32(o.Version), Seq: o.Seq,
		State: o.State, Value: Struct(o.Value), Labels: o.Labels, By: o.By, At: Time(o.At)}
}

// ChangeObjectsToPB converts versions of change objects.
func ChangeObjectsToPB(os []domain.ChangeObject) []*graphv1.ChangeObject {
	out := make([]*graphv1.ChangeObject, len(os))
	for i, o := range os {
		out[i] = ChangeObjectToPB(o)
	}
	return out
}

// ChangeObjectsFromPB is the inverse of ChangeObjectsToPB.
func ChangeObjectsFromPB(os []*graphv1.ChangeObject) []domain.ChangeObject {
	out := make([]domain.ChangeObject, len(os))
	for i, o := range os {
		out[i] = domain.ChangeObject{Change: domain.ChangeID(o.ChangeId), Type: o.Type, Key: o.Key, Workspace: o.Workspace, Version: int(o.Version), Seq: o.Seq,
			State: o.State, Value: Map(o.Value), Labels: labelsOrNil(o.Labels), By: o.By, At: FromTime(o.At)}
	}
	return out
}

// ObjectWritesToPB converts writes of change objects.
func ObjectWritesToPB(ws []domain.ObjectWrite) []*graphv1.ObjectWrite {
	out := make([]*graphv1.ObjectWrite, len(ws))
	for i, w := range ws {
		out[i] = &graphv1.ObjectWrite{Type: w.Type, Key: w.Key, Value: Struct(w.Value), Merge: w.Merge, Transition: w.Transition, Workspace: w.Workspace, Labels: w.Labels}
		if w.Expect != nil {
			e := int32(*w.Expect)
			out[i].Expect = &e
		}
	}
	return out
}

// ObjectWritesFromPB is the inverse of ObjectWritesToPB.
func ObjectWritesFromPB(ws []*graphv1.ObjectWrite) []domain.ObjectWrite {
	out := make([]domain.ObjectWrite, len(ws))
	for i, w := range ws {
		out[i] = domain.ObjectWrite{Type: w.Type, Key: w.Key, Value: Map(w.Value), Merge: w.Merge, Transition: w.Transition, Workspace: w.Workspace, Labels: labelsOrNil(w.Labels)}
		if w.Expect != nil {
			e := int(*w.Expect)
			out[i].Expect = &e
		}
	}
	return out
}
