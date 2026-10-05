package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

// LogEntryToPB converts an entry of the log of a change (ADR 0030).
func LogEntryToPB(e domain.LogEntry) *graphv1.LogEntry {
	return &graphv1.LogEntry{Seq: e.Seq, Id: e.ID, ChangeId: string(e.Change), Type: e.Type, Flow: e.Flow, ProcessId: e.Process,
		Execution: e.Execution, Subject: e.Subject, By: e.By, At: Time(e.At), Payload: string(e.Payload)}
}

// LogEntryFromPB converts a protobuf entry of the log of a change.
func LogEntryFromPB(e *graphv1.LogEntry) domain.LogEntry {
	return domain.LogEntry{Seq: e.Seq, ID: e.Id, Change: domain.ChangeID(e.ChangeId), Type: e.Type, Flow: e.Flow, Process: e.ProcessId,
		Execution: e.Execution, Subject: e.Subject, By: e.By, At: FromTime(e.At), Payload: []byte(e.Payload)}
}

// LogEntriesFromPB converts protobuf entries.
func LogEntriesFromPB(es []*graphv1.LogEntry) []domain.LogEntry {
	out := make([]domain.LogEntry, len(es))
	for i, e := range es {
		out[i] = LogEntryFromPB(e)
	}
	return out
}
