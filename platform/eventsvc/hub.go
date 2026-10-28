// Package eventsvc is the one event stream of the web (ADR 0053): the facts the platform publishes (processes,
// changes, nodes, commits, registry) as thin, ordered events a client keeps its state from, and the presence of
// the users looking at them.
package eventsvc

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	eventsv1 "github.com/zimwip/goap/contracts/gen/goap/events/v1"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/enginesvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/mcp/mcpsvc"
	"github.com/zimwip/goap/platform/convsvc"
	"github.com/zimwip/goap/platform/jobsvc"
	"github.com/zimwip/goap/platform/modelgw"
	"github.com/zimwip/goap/platform/prefssvc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Defaults of the hub.
const (
	DefaultHistory     = 5000
	DefaultPresenceTTL = 30 * time.Second
	subscriberBuffer   = 512
	maxProcesses       = 20000
)

// Hub receives the platform's publications (it is an engine.Publisher / graph.EventSink, and Ingest takes them
// from NATS), turns them into ordered events, keeps a short history to resume streams, and holds the presence.
type Hub struct {
	// Authz decides which process events a caller may read; nil allows every one.
	Authz authz.Authorizer
	// Lookup finds the process of a log line that arrives before any event of its process (optional).
	Lookup func(ctx context.Context, id string) (engine.Process, bool)
	// History is the number of events kept to resume a stream (DefaultHistory when zero).
	History int
	// PresenceTTL is how long a tab stays present without a heartbeat (DefaultPresenceTTL when zero).
	PresenceTTL time.Duration
	// Relay tells the other replicas of a presence change (nil: a single replica). The subjects are under
	// PresenceSubject, the message is JSON.
	Relay func(subject string, v any)
	// Now is the clock (time.Now when nil).
	Now func() time.Time

	mu       sync.Mutex
	epoch    string
	seq      uint64
	ring     []*record // oldest first
	subs     map[int]*subscriber
	next     int
	procs    map[string]procScope // process id -> who may read it
	personal map[string]string    // personal change id -> its subject
	tabs     map[string]*tab      // subject/tab id -> where it looks
}

// record is an event with what decides who may see it.
type record struct {
	ev   *eventsv1.Event
	proc string // process the event is about (process events)
	// personalTo is the subject a personal change's events are for ("" when they are public)
	personalTo string
}

type procScope struct {
	id, methodology, org, owner, project string
}

type subscriber struct {
	// wake says the history grew: the stream reads what follows its cursor from the ring (Hub.after), so a burst of
	// events (a git import commits thousands of nodes) never cuts it; it only falls behind the history when the
	// client reads slower than the platform writes for longer than History events.
	wake chan struct{}
	// cursor is the seq of the last event the stream took from the ring.
	cursor uint64
	// ch carries the presence events, which are live only (not numbered, not kept); one that cannot keep up is cut.
	ch       chan *record
	overflow bool
}

type tab struct {
	subject, id, kind, loc string
	since, seen            time.Time
	// local: the tab is connected to this replica (the others learn of it through the relay)
	local bool
}

// NewHub returns a hub with a fresh epoch.
func NewHub() *Hub {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &Hub{epoch: hex.EncodeToString(b[:]), subs: map[int]*subscriber{}, procs: map[string]procScope{},
		personal: map[string]string{}, tabs: map[string]*tab{}}
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Hub) history() int {
	if h.History > 0 {
		return h.History
	}
	return DefaultHistory
}

func (h *Hub) presenceTTL() time.Duration {
	if h.PresenceTTL > 0 {
		return h.PresenceTTL
	}
	return DefaultPresenceTTL
}

// Epoch names this stream: a seq only means something within it.
func (h *Hub) Epoch() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epoch
}

// WithCommand returns a context carrying the id of the client command being served and who issued it: the
// events published under it are stamped with both (and the bus carries the stamp, platform.Stamp).
func WithCommand(ctx context.Context, actor, command string) context.Context {
	return platform.WithStamp(ctx, platform.Stamp{Actor: actor, Command: command})
}

func stampOf(ctx context.Context) (actor, command string) {
	st := platform.StampOf(ctx)
	actor, command = st.Actor, st.Command
	if actor == "" {
		actor = authz.From(ctx).Subject
	}
	return actor, command
}

// Publish implements engine.Publisher and graph.EventSink: the in-process way in.
func (h *Hub) Publish(ctx context.Context, subject string, v any) error {
	actor, command := stampOf(ctx)
	h.accept(subject, v, actor, command)
	return nil
}

// Ingest takes a message of the bus (NATS) by its subject, with the stamp its publisher put on it.
func (h *Hub) Ingest(subject string, data []byte, st platform.Stamp) {
	if strings.HasPrefix(subject, PresenceSubject) {
		h.ingestPresence(subject, data)
		return
	}
	var v any
	switch {
	case strings.HasPrefix(subject, "goap.process."):
		var ev engine.ProcessEvent
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.node."):
		var ev domain.NodeEvent
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.commit."):
		var ev domain.CommitEvent
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.change."), strings.HasPrefix(subject, "goap.changed."):
		var ev domain.ChangeEvent
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.conversation."):
		var ev convsvc.Event
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.registry."):
		var m map[string]string
		if json.Unmarshal(data, &m) != nil {
			return
		}
		v = m
	case strings.HasPrefix(subject, "goap.job."):
		var ev jobsvc.Event
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.mcp.connector."):
		var ev mcpsvc.ConnectorEvent
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.model.call."):
		var ev modelgw.CallRecorded
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.model.quota."):
		var ev modelgw.QuotaExceeded
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	case strings.HasPrefix(subject, "goap.preferences."):
		var ev prefssvc.Event
		if json.Unmarshal(data, &ev) != nil {
			return
		}
		v = ev
	default:
		return
	}
	h.accept(subject, v, st.Actor, st.Command)
}

// accept turns a publication into an event; what is not for the web is dropped.
func (h *Hub) accept(subject string, v any, actor, command string) {
	r := &record{ev: &eventsv1.Event{Actor: actor, CommandId: command}}
	ev := r.ev
	h.mu.Lock()
	defer h.mu.Unlock()
	switch m := v.(type) {
	case engine.ProcessEvent:
		ev.Kind, ev.Type = "process", "process."+m.Event
		switch {
		case m.Process != nil:
			p := m.Process
			ev.Id, ev.Project, ev.ChangeId = p.ID, p.Project, string(p.ChangeID)
			ev.Process = enginesvc.ProcessToPB(p)
			h.rememberProcess(procScope{id: p.ID, methodology: p.Methodology, org: p.Initiator.Org, owner: p.Initiator.Subject, project: p.Project})
			r.proc = p.ID
		case m.Log != nil:
			ev.Id = m.Log.ProcessID
			ev.Log = enginesvc.LogToPB(*m.Log)
			ev.Type = "process.log"
			r.proc = m.Log.ProcessID
		default:
			return
		}
		ev.Time = pbconv.Time(m.Time)
	case domain.NodeEvent:
		ev.Kind, ev.Type = "node", "node.written"
		if m.Deleted {
			ev.Type = "node.deleted"
		}
		ev.Id, ev.Namespace, ev.Branch, ev.ChangeId, ev.Version = string(m.ID), m.Namespace, m.Branch, string(m.ChangeID), int64(m.Version)
		ev.Time = pbconv.Time(m.Time)
		r.personalTo = h.personal[string(m.ChangeID)]
	case domain.CommitEvent:
		ev.Kind, ev.Type = "commit", "commit.advanced"
		ev.Id, ev.Branch, ev.Namespace = string(m.ID), m.Branch, m.Namespace
		ev.Time = pbconv.Time(m.Time)
	case domain.ChangeEvent:
		c := m.Change
		ev.Kind = "change"
		ev.Type = changeType(m.Type, subject)
		ev.Id, ev.Namespace, ev.Branch, ev.Project, ev.ChangeId = string(c.ID), c.Namespace, c.Branch, c.ProjectID.Key, string(c.ID)
		ev.Version = c.Seq // the position the write left the log of the change at (ADR 0153), 0 when it wrote none
		ev.Time = pbconv.Time(h.now())
		if access.IsPersonal(c) {
			h.personal[string(c.ID)] = access.PersonalSubjectOf(c)
		}
		r.personalTo = h.personal[string(c.ID)]
		if ev.Type == "change.purged" {
			delete(h.personal, string(c.ID))
		}
	case convsvc.Event:
		// goap.conversation.<id>.<event> (ADR 0136): the owner's alone, like a personal change; an event naming no
		// owner is for nobody
		if m.Subject == "" || m.ConversationID == "" {
			return
		}
		ev.Kind, ev.Type = "conversation", "conversation."+m.Event
		ev.Id, ev.Label = m.ConversationID, m.MessageID
		ev.Time = pbconv.Time(m.At)
		if m.At.IsZero() {
			ev.Time = pbconv.Time(h.now())
		}
		r.personalTo = m.Subject
	case map[string]string:
		// goap.registry.<methodology|domain>.<event>
		parts := strings.Split(subject, ".")
		if len(parts) < 4 {
			return
		}
		ev.Kind, ev.Type = parts[2], strings.Join(parts[2:], ".")
		ev.Id, ev.Namespace, ev.Label = m["name"], m["name"], m["version"]
		ev.Time = pbconv.Time(h.now())
	case jobsvc.Event:
		// goap.job.<id>.<created|started|ended> (ADR 0149): the person's own, like a conversation; the label is the
		// kind of the job, the namespace its target. Its progress travels on the job stream (GET /api/jobs/events).
		if m.ID == "" || m.By == "" {
			return
		}
		ev.Kind, ev.Type = "job", "job."+subject[strings.LastIndex(subject, ".")+1:]
		ev.Id, ev.Label, ev.Namespace = m.ID, m.Kind, m.Target
		ev.Time = h.timeOr(m.At)
		r.personalTo = m.By
	case mcpsvc.ConnectorEvent:
		// goap.mcp.connector.<id>.<registered|lost|back>: what a connector is, never a secret
		ev.Kind, ev.Type = "connector", "connector."+m.Event
		ev.Id, ev.Label = m.Connector, m.Version
		ev.Time = h.timeOr(m.At)
	case modelgw.CallRecorded:
		// goap.model.call.<seq>.recorded (ADR 0089): a row of the ledger, for the person it is attributed to (ADR 0114)
		if r.personalTo = cmp.Or(m.EndUser, m.Subject); r.personalTo == "" {
			return
		}
		ev.Kind, ev.Type = "model", "model.call"
		ev.Id, ev.Label = strconv.FormatInt(m.Seq, 10), cmp.Or(m.Alias, m.Provider+"/"+m.Model)
		ev.Project, ev.ChangeId = m.Project, m.ChangeID
		ev.Time = h.timeOr(m.At)
	case modelgw.QuotaExceeded:
		if r.personalTo = cmp.Or(m.EndUser, m.Subject); r.personalTo == "" {
			return
		}
		ev.Kind, ev.Type = "model", "model.quota"
		ev.Id, ev.Label = strconv.FormatInt(m.Seq, 10), m.Provider+"/"+m.Model
		ev.Time = h.timeOr(m.At)
	case prefssvc.Event:
		// goap.preferences.<subject>.<updated|reset> (ADR 0038): the other tabs of the person follow
		if m.Subject == "" {
			return
		}
		ev.Kind, ev.Type = "preferences", "preferences."+subject[strings.LastIndex(subject, ".")+1:]
		ev.Id, ev.Label = m.Subject, strings.Join(m.Keys, ",")
		ev.Time = h.timeOr(m.At)
		r.personalTo = m.Subject
	default:
		return
	}
	h.emit(r)
}

// timeOr is the time of a payload, else now.
func (h *Hub) timeOr(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		t = h.now()
	}
	return pbconv.Time(t)
}

func changeType(t, subject string) string {
	if t != "" {
		return t
	}
	return "change." + subject[strings.LastIndex(subject, ".")+1:]
}

func (h *Hub) rememberProcess(p procScope) {
	if len(h.procs) >= maxProcesses {
		clear(h.procs) // bounded; a log line of a forgotten process is looked up again
	}
	h.procs[p.id] = p
}

// emit numbers an event, keeps it for resuming, and delivers it. Call with h.mu held.
func (h *Hub) emit(r *record) {
	h.seq++
	r.ev.Epoch, r.ev.Seq = h.epoch, h.seq
	h.ring = append(h.ring, r)
	if n := h.history(); len(h.ring) > n {
		h.ring = append(h.ring[:0:0], h.ring[len(h.ring)-n:]...)
	}
	for _, s := range h.subs {
		select {
		case s.wake <- struct{}{}:
		default: // already told
		}
	}
}

// after returns the events numbered after seq, or false when the history no longer holds them all.
func (h *Hub) after(seq uint64) ([]*record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.afterLocked(seq)
}

func (h *Hub) afterLocked(seq uint64) ([]*record, bool) {
	if seq >= h.seq {
		return nil, seq == h.seq
	}
	oldest := h.seq + 1
	if len(h.ring) > 0 {
		oldest = h.ring[0].ev.Seq
	}
	if seq+1 < oldest {
		return nil, false
	}
	i := sort.Search(len(h.ring), func(i int) bool { return h.ring[i].ev.Seq > seq })
	return append([]*record(nil), h.ring[i:]...), true
}

// deliver sends a presence record to every subscriber; one that cannot keep up is cut (it reconnects and starts
// from a new snapshot).
func (h *Hub) deliver(r *record) {
	for id, s := range h.subs {
		select {
		case s.ch <- r:
		default:
			s.overflow = true
			close(s.ch)
			delete(h.subs, id)
		}
	}
}

// Presence of one tab.
type Presence = eventsv1.Presence

func (t *tab) pb() *eventsv1.Presence {
	return &eventsv1.Presence{TabId: t.id, Subject: t.subject, Kind: t.kind, Id: t.loc, Since: pbconv.Time(t.since)}
}

// PresenceSubject prefixes the bus subjects presence travels on between replicas: outside goap.>, so the GOAP
// stream does not persist it (core NATS, at most once).
const PresenceSubject = "presence."

// presenceMsg is what replicas tell each other.
type presenceMsg struct {
	Origin  string `json:"origin"`
	Subject string `json:"subject,omitempty"`
	Tab     string `json:"tab,omitempty"`
	Kind    string `json:"kind,omitempty"`
	ID      string `json:"id,omitempty"`
}

func (h *Hub) relay(subject string, m presenceMsg) {
	if h.Relay != nil {
		m.Origin = h.origin()
		h.Relay(subject, m)
	}
}

func (h *Hub) origin() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epoch
}

// Heartbeat says where a tab of a subject, connected to this replica, looks.
func (h *Hub) Heartbeat(subject, tabID, kind, id string) {
	if h.beat(subject, tabID, kind, id, true) {
		h.relay(PresenceSubject+"beat", presenceMsg{Subject: subject, Tab: tabID, Kind: kind, ID: id})
	}
}

// beat records a tab (a local one, or one relayed by another replica); it reports whether it is valid.
func (h *Hub) beat(subject, tabID, kind, id string, local bool) bool {
	if subject == "" || tabID == "" {
		return false
	}
	now := h.now()
	key := subject + "/" + tabID
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tabs[key]
	typ := ""
	switch {
	case !ok:
		t = &tab{subject: subject, id: tabID, since: now}
		h.tabs[key] = t
		typ = "presence.joined"
	case t.kind != kind || t.loc != id:
		typ = "presence.moved"
		t.since = now
	}
	t.kind, t.loc, t.seen = kind, id, now
	t.local = t.local || local
	if typ != "" {
		h.presenceEvent(typ, t)
	}
	return true
}

// Leave drops a tab of this replica.
func (h *Hub) Leave(subject, tabID string) {
	if h.drop(subject, tabID) {
		h.relay(PresenceSubject+"leave", presenceMsg{Subject: subject, Tab: tabID})
	}
}

func (h *Hub) drop(subject, tabID string) bool {
	key := subject + "/" + tabID
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tabs[key]
	if ok {
		delete(h.tabs, key)
		h.presenceEvent("presence.left", t)
	}
	return ok
}

// Hello asks the other replicas to tell the tabs they hold: a replica that just started learns the picture.
func (h *Hub) Hello() { h.relay(PresenceSubject+"hello", presenceMsg{}) }

// ingestPresence applies what another replica relayed (its own messages come back too: ignored).
func (h *Hub) ingestPresence(subject string, data []byte) {
	var m presenceMsg
	if json.Unmarshal(data, &m) != nil || m.Origin == h.origin() {
		return
	}
	switch strings.TrimPrefix(subject, PresenceSubject) {
	case "beat":
		h.beat(m.Subject, m.Tab, m.Kind, m.ID, false)
	case "leave":
		h.drop(m.Subject, m.Tab)
	case "hello":
		h.mu.Lock()
		var mine []presenceMsg
		for _, t := range h.tabs {
			if t.local {
				mine = append(mine, presenceMsg{Subject: t.subject, Tab: t.id, Kind: t.kind, ID: t.loc})
			}
		}
		h.mu.Unlock()
		for _, b := range mine {
			h.relay(PresenceSubject+"beat", b)
		}
	}
}

// Expire drops the tabs with no heartbeat for the TTL.
func (h *Hub) Expire() {
	cut := h.now().Add(-h.presenceTTL())
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, t := range h.tabs {
		if t.seen.Before(cut) {
			delete(h.tabs, key)
			h.presenceEvent("presence.left", t)
		}
	}
}

// Run expires the presence of closed tabs until ctx is done.
func (h *Hub) Run(ctx context.Context) {
	tick := time.NewTicker(h.presenceTTL() / 3)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.Expire()
		}
	}
}

// presenceEvent delivers a presence change; it is live only (not numbered, not kept): a stream starts from a snapshot.
func (h *Hub) presenceEvent(typ string, t *tab) {
	h.deliver(&record{ev: &eventsv1.Event{Epoch: h.epoch, Type: typ, Kind: "presence", Id: t.loc,
		Time: pbconv.Time(h.now()), Presence: []*eventsv1.Presence{t.pb()}}, personalTo: h.personalOf(t)})
}

// personalOf is the subject only whom a tab on a personal change may be shown to.
func (h *Hub) personalOf(t *tab) string {
	if t.kind == "change" {
		return h.personal[t.loc]
	}
	return ""
}
