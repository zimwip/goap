package eventsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	eventsv1 "github.com/zimwip/goap/gen/goap/events/v1"
	"github.com/zimwip/goap/gen/goap/events/v1/eventsv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
)

func node(id string) domain.NodeEvent {
	return domain.NodeEvent{ID: domain.NodeID(id), Version: 1, Branch: "main", Namespace: "alm", Type: "alm@Requirement", Time: time.Now()}
}

func publish(t *testing.T, h *Hub, subject string, v any) {
	t.Helper()
	if err := h.Publish(context.Background(), subject, v); err != nil {
		t.Fatal(err)
	}
}

func seqs(rs []*record) []uint64 {
	var out []uint64
	for _, r := range rs {
		out = append(out, r.ev.Seq)
	}
	return out
}

func TestResumeFromSeq(t *testing.T) {
	h := NewHub()
	for _, id := range []string{"a", "b", "c"} {
		publish(t, h, "goap.node.alm.x."+id+".written", node(id))
	}
	_, backlog, resync, cancel := h.subscribe(h.Epoch(), 1)
	defer cancel()
	if resync {
		t.Fatal("a stream inside the history must resume, not resync")
	}
	if got := seqs(backlog); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("backlog = %v, want [2 3]", got)
	}
}

func TestResyncOnOtherEpochOrGap(t *testing.T) {
	h := NewHub()
	h.History = 2
	for _, id := range []string{"a", "b", "c", "d"} {
		publish(t, h, "goap.node.alm.x."+id+".written", node(id))
	}
	for name, c := range map[string]struct {
		epoch string
		after uint64
		want  bool
	}{
		"first connection": {"", 0, false},
		"other epoch":      {"elsewhere", 3, true},
		"history gap":      {h.Epoch(), 1, true},
		"in history":       {h.Epoch(), 2, false},
		"from the future":  {h.Epoch(), 99, true},
	} {
		_, _, resync, cancel := h.subscribe(c.epoch, c.after)
		cancel()
		if resync != c.want {
			t.Errorf("%s: resync = %v, want %v", name, resync, c.want)
		}
	}
}

type denyOthers struct{}

func (denyOthers) Authorize(_ context.Context, r authz.Request) (bool, error) {
	return r.Subject.Subject == r.Resource.Owner, nil
}

func TestVisibility(t *testing.T) {
	h := NewHub()
	h.Authz = denyOthers{}
	ctx := context.Background()
	publish(t, h, "goap.process.p1.started", engine.ProcessEvent{Event: "started", Time: time.Now(),
		Process: &engine.Process{ID: "p1", Methodology: "sdlc", Initiator: authz.Principal{Subject: "alice"}}})
	publish(t, h, "goap.process.p1.log", engine.ProcessEvent{Event: "log", Time: time.Now(), Log: &engine.LogLine{ProcessID: "p1", Message: "hi"}})
	publish(t, h, "goap.change.C1.created", domain.ChangeEvent{Type: "change.created", Change: domain.Change{ID: "C1", OwnerOrg: "USR:alice"}})
	publish(t, h, "goap.node.alm.x.n1.written", func() domain.NodeEvent { e := node("n1"); e.ChangeID = "C1"; return e }())
	publish(t, h, "goap.node.alm.x.n2.written", node("n2"))

	count := func(who string) (n int) {
		v := h.visibility(ctx, authz.Principal{Subject: who})
		h.mu.Lock()
		ring := append([]*record(nil), h.ring...)
		h.mu.Unlock()
		for _, r := range ring {
			if v.sees(ctx, r) {
				n++
			}
		}
		return n
	}
	if got := count("alice"); got != 5 {
		t.Errorf("alice sees %d events, want all 5", got)
	}
	// bob: not the process (2 events), not the personal change nor its node (2), only the public node
	if got := count("bob"); got != 1 {
		t.Errorf("bob sees %d events, want 1", got)
	}
}

func TestCommandStamp(t *testing.T) {
	h := NewHub()
	ctx := WithCommand(context.Background(), "alice", "cmd-1")
	if err := h.Publish(ctx, "goap.node.alm.x.n.written", node("n")); err != nil {
		t.Fatal(err)
	}
	ev := h.ring[0].ev
	if ev.Actor != "alice" || ev.CommandId != "cmd-1" {
		t.Fatalf("stamp = %q/%q", ev.Actor, ev.CommandId)
	}
}

func TestPresenceLifecycle(t *testing.T) {
	h := NewHub()
	now := time.Now()
	h.Now = func() time.Time { return now }
	s, _, _, cancel := h.subscribe("", 0)
	defer cancel()
	h.Heartbeat("alice", "t1", "node", "N1")
	h.Heartbeat("alice", "t1", "node", "N1") // nothing new
	h.Heartbeat("alice", "t1", "change", "C1")
	h.Heartbeat("bob", "t1", "node", "N1")
	var types []string
	for len(s.ch) > 0 {
		types = append(types, (<-s.ch).ev.Type)
	}
	want := []string{"presence.joined", "presence.moved", "presence.joined"}
	if len(types) != len(want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
	if got := len(h.snapshot(h.visibility(context.Background(), authz.Principal{Subject: "bob"})).Presence); got != 2 {
		t.Fatalf("snapshot has %d tabs, want 2", got)
	}
	// tabs expire without heartbeat, a left tab goes at once
	now = now.Add(DefaultPresenceTTL / 2)
	h.Heartbeat("bob", "t1", "node", "N1")
	now = now.Add(DefaultPresenceTTL/2 + time.Second)
	h.Expire()
	if got := len(h.tabs); got != 1 {
		t.Fatalf("after expiry %d tabs, want bob's only", got)
	}
	h.Leave("bob", "t1")
	if len(h.tabs) != 0 {
		t.Fatal("a tab that left must go at once")
	}
}

func TestSlowSubscriberIsCut(t *testing.T) {
	h := NewHub()
	s, _, _, cancel := h.subscribe("", 0)
	defer cancel()
	for i := 0; i < subscriberBuffer+1; i++ {
		publish(t, h, "goap.node.alm.x.n.written", node("n"))
	}
	if !s.overflow {
		t.Fatal("a subscriber that cannot keep up must be cut")
	}
}

func TestWatchStream(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(mux(&Handler{Hub: h, Idle: 20 * time.Millisecond,
		Identity: identity.Extractor{Default: &authz.Principal{Subject: "alice"}}}))
	defer srv.Close()
	client := eventsv1connect.NewEventServiceClient(http.DefaultClient, srv.URL)
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	st, err := client.Watch(ctx, connect.NewRequest(&eventsv1.WatchRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	next := func() *eventsv1.Event {
		if !st.Receive() {
			t.Fatalf("stream ended: %v", st.Err())
		}
		return st.Msg()
	}
	if ev := next(); ev.Type != "presence.snapshot" {
		t.Fatalf("first event = %s, want the presence snapshot", ev.Type)
	}
	publish(t, h, "goap.node.alm.x.n.written", node("n"))
	for ev := next(); ; ev = next() {
		if ev.Type == "node.written" {
			if ev.Seq != 1 || ev.Epoch != h.Epoch() {
				t.Fatalf("event = %+v", ev)
			}
			break
		}
		if ev.Type != "heartbeat" {
			t.Fatalf("unexpected %s", ev.Type)
		}
	}
	// a tab announces itself through the same service
	if _, err := client.Heartbeat(ctx, connect.NewRequest(&eventsv1.HeartbeatRequest{TabId: "t", Kind: "node", Id: "n"})); err != nil {
		t.Fatal(err)
	}
	for ev := next(); ; ev = next() {
		if ev.Type == "presence.joined" {
			if ev.Presence[0].Subject != "alice" {
				t.Fatalf("presence = %+v", ev.Presence)
			}
			return
		}
	}
}

func mux(h *Handler) http.Handler {
	m := http.NewServeMux()
	path, handler := eventsv1connect.NewEventServiceHandler(h)
	m.Handle(path, handler)
	return m
}

func TestPresenceRelayBetweenReplicas(t *testing.T) {
	a, b := NewHub(), NewHub()
	var hubs []*Hub
	join := func(h *Hub) {
		hubs = append(hubs, h)
		h.Relay = func(subject string, v any) { // what NATS does: every replica gets it (its own too)
			data, _ := json.Marshal(v)
			for _, o := range hubs {
				o.Ingest(subject, data, platform.Stamp{})
			}
		}
	}
	join(a)
	join(b)
	sb, _, _, cancel := b.subscribe("", 0)
	defer cancel()
	// alice is connected to replica a; replica b's viewers see her
	a.Heartbeat("alice", "t1", "node", "N1")
	if got := len(b.snapshot(b.visibility(context.Background(), authz.Principal{Subject: "bob"})).Presence); got != 1 {
		t.Fatalf("replica b sees %d tabs, want alice's", got)
	}
	if ev := <-sb.ch; ev.ev.Type != "presence.joined" {
		t.Fatalf("event = %s", ev.ev.Type)
	}
	if !a.tabs["alice/t1"].local || b.tabs["alice/t1"].local {
		t.Fatal("only the replica she is connected to holds her tab as local")
	}
	// a replica that just started learns the tabs of the others
	c := NewHub()
	join(c)
	c.Hello()
	if got := len(c.tabs); got != 1 {
		t.Fatalf("a replica that just started learns %d tabs, want 1", got)
	}
	a.Leave("alice", "t1")
	if len(a.tabs)+len(b.tabs)+len(c.tabs) != 0 {
		t.Fatal("a tab that left goes from every replica")
	}
}

func TestStampTravelsOnTheBus(t *testing.T) {
	h := NewHub()
	data, _ := json.Marshal(node("n"))
	h.Ingest("goap.node.alm.x.n.written", data, platform.Stamp{Actor: "alice", Command: "cmd-9"})
	if ev := h.ring[0].ev; ev.Actor != "alice" || ev.CommandId != "cmd-9" {
		t.Fatalf("stamp = %q/%q", ev.Actor, ev.CommandId)
	}
}
