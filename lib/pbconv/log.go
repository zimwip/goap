package pbconv

import (
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
)

// LogEntryToPB converts an entry of the log of a change (ADR 0030).
func LogEntryToPB(e domain.LogEntry) *graphv1.LogEntry {
	return &graphv1.LogEntry{Seq: e.Seq, Id: e.ID, ChangeId: string(e.Change), Type: e.Type, Flow: e.Flow, ProcessId: e.Process,
		Execution: e.Execution, Subject: e.Subject, By: e.By, At: Time(e.At), Payload: string(e.Payload), Causation: e.Causation}
}

// LogEntryFromPB converts a protobuf entry of the log of a change.
func LogEntryFromPB(e *graphv1.LogEntry) domain.LogEntry {
	return domain.LogEntry{Seq: e.Seq, ID: e.Id, Change: domain.ChangeID(e.ChangeId), Type: e.Type, Flow: e.Flow, Process: e.ProcessId,
		Execution: e.Execution, Subject: e.Subject, By: e.By, At: FromTime(e.At), Payload: []byte(e.Payload), Causation: e.Causation}
}

// LogEntriesFromPB converts protobuf entries.
func LogEntriesFromPB(es []*graphv1.LogEntry) []domain.LogEntry {
	out := make([]domain.LogEntry, len(es))
	for i, e := range es {
		out[i] = LogEntryFromPB(e)
	}
	return out
}
