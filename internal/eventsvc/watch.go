package eventsvc

import (
	"context"
	"sort"
	"time"

	"connectrpc.com/connect"

	eventsv1 "github.com/zimwip/goap/gen/goap/events/v1"
	"github.com/zimwip/goap/gen/goap/events/v1/eventsv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
)

// HeartbeatEvery is how often an idle stream says it is alive.
const HeartbeatEvery = 15 * time.Second

// Handler implements eventsv1connect.EventServiceHandler.
type Handler struct {
	Hub      *Hub
	Identity identity.Extractor
	// Idle overrides HeartbeatEvery (tests).
	Idle time.Duration
}

var _ eventsv1connect.EventServiceHandler = (*Handler)(nil)

// CommandInterceptor stamps the events published while a request is served with the id its client gave it
// (X-Goap-Command) and the caller, so a client recognises the echo of its own writes. Wrap the services that write.
func CommandInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if c := req.Header().Get(HeaderCommand); c != "" {
				ctx = WithCommand(ctx, identity.FromHeaders(req.Header()).Subject, c)
			}
			return next(ctx, req)
		}
	})
}

// HeaderCommand is the header a client names its command with.
const HeaderCommand = "X-Goap-Command"

// Watch streams the events the caller may read.
func (h *Handler) Watch(ctx context.Context, r *connect.Request[eventsv1.WatchRequest], stream *connect.ServerStream[eventsv1.Event]) error {
	ctx = h.Identity.Context(ctx, r.Header())
	who := authz.From(ctx)
	if who.Anonymous() {
		return connect.NewError(connect.CodeUnauthenticated, errAnonymous)
	}
	sub, backlog, resync, cancel := h.Hub.subscribe(r.Msg.Epoch, r.Msg.AfterSeq)
	defer cancel()
	v := h.Hub.visibility(ctx, who)
	send := func(ev *eventsv1.Event) error { return stream.Send(ev) }
	if resync {
		if err := send(&eventsv1.Event{Epoch: h.Hub.Epoch(), Type: "resync", Time: pbconv.Time(h.Hub.now())}); err != nil {
			return err
		}
	}
	// a stream starts from where everybody looks
	if err := send(h.Hub.snapshot(v)); err != nil {
		return err
	}
	for _, rec := range backlog {
		if v.sees(ctx, rec) {
			if err := send(rec.ev); err != nil {
				return err
			}
		}
	}
	every := h.Idle
	if every <= 0 {
		every = HeartbeatEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := send(&eventsv1.Event{Epoch: h.Hub.Epoch(), Seq: h.Hub.Seq(), Type: "heartbeat", Time: pbconv.Time(h.Hub.now())}); err != nil {
				return err
			}
		case rec, ok := <-sub.ch:
			if !ok {
				// too slow to follow: say so, the client reconnects and refetches
				return send(&eventsv1.Event{Epoch: h.Hub.Epoch(), Type: "resync", Time: pbconv.Time(h.Hub.now())})
			}
			if v.sees(ctx, rec) {
				if err := send(rec.ev); err != nil {
					return err
				}
			}
		}
	}
}

// Heartbeat records where a tab of the caller looks.
func (h *Handler) Heartbeat(ctx context.Context, r *connect.Request[eventsv1.HeartbeatRequest]) (*connect.Response[eventsv1.HeartbeatResponse], error) {
	who := authz.From(h.Identity.Context(ctx, r.Header()))
	if who.Anonymous() {
		return nil, connect.NewError(connect.CodeUnauthenticated, errAnonymous)
	}
	h.Hub.Heartbeat(who.Subject, r.Msg.TabId, r.Msg.Kind, r.Msg.Id)
	return connect.NewResponse(&eventsv1.HeartbeatResponse{}), nil
}

// Leave drops a tab of the caller.
func (h *Handler) Leave(ctx context.Context, r *connect.Request[eventsv1.LeaveRequest]) (*connect.Response[eventsv1.LeaveResponse], error) {
	who := authz.From(h.Identity.Context(ctx, r.Header()))
	if who.Anonymous() {
		return nil, connect.NewError(connect.CodeUnauthenticated, errAnonymous)
	}
	h.Hub.Leave(who.Subject, r.Msg.TabId)
	return connect.NewResponse(&eventsv1.LeaveResponse{}), nil
}

// Seq is the number of the last event.
func (h *Hub) Seq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// subscribe registers a stream and returns what it missed since (epoch, after), or says it must resync.
func (h *Hub) subscribe(epoch string, after uint64) (*subscriber, []*record, bool, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &subscriber{ch: make(chan *record, subscriberBuffer)}
	id := h.next
	h.next++
	h.subs[id] = s
	var backlog []*record
	resync := false
	switch {
	case epoch == "":
		// a first connection: nothing to catch up
	case epoch != h.epoch:
		resync = true
	case after > h.seq:
		resync = true
	default:
		oldest := h.seq + 1
		if len(h.ring) > 0 {
			oldest = h.ring[0].ev.Seq
		}
		if after+1 < oldest {
			resync = true
			break
		}
		i := sort.Search(len(h.ring), func(i int) bool { return h.ring[i].ev.Seq > after })
		backlog = append(backlog, h.ring[i:]...)
	}
	return s, backlog, resync, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(s.ch)
		}
	}
}

// snapshot is the presence a stream starts from, as the caller may see it.
func (h *Hub) snapshot(v *viewer) *eventsv1.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	ev := &eventsv1.Event{Epoch: h.epoch, Seq: h.seq, Type: "presence.snapshot", Kind: "presence", Time: pbconv.Time(h.now())}
	for _, t := range h.tabs {
		if p := h.personalOf(t); p == "" || p == v.who.Subject {
			ev.Presence = append(ev.Presence, t.pb())
		}
	}
	sort.Slice(ev.Presence, func(i, j int) bool {
		a, b := ev.Presence[i], ev.Presence[j]
		return a.Subject+a.TabId < b.Subject+b.TabId
	})
	return ev
}

// viewer decides what a caller may see, remembering the decisions of its stream.
type viewer struct {
	h       *Hub
	who     authz.Principal
	allowed map[string]bool
}

func (h *Hub) visibility(_ context.Context, who authz.Principal) *viewer {
	return &viewer{h: h, who: who, allowed: map[string]bool{}}
}

// sees reports whether the caller may read an event: the events of a personal change are its subject's alone,
// those of a process are decided by the process's read rule, the others are open to every signed-in caller (the
// graph's reads are, a client refetches through the services, which decide again).
func (v *viewer) sees(ctx context.Context, r *record) bool {
	if r.personalTo != "" && r.personalTo != v.who.Subject {
		return false
	}
	if r.proc == "" {
		return true
	}
	if ok, seen := v.allowed[r.proc]; seen {
		return ok
	}
	ok := v.h.canReadProcess(ctx, v.who, r.proc)
	v.allowed[r.proc] = ok
	return ok
}

func (h *Hub) canReadProcess(ctx context.Context, who authz.Principal, id string) bool {
	h.mu.Lock()
	p, known := h.procs[id]
	h.mu.Unlock()
	if !known && h.Lookup != nil {
		if pr, ok := h.Lookup(ctx, id); ok {
			p = procScope{id: pr.ID, methodology: pr.Methodology, org: pr.Initiator.Org, owner: pr.Initiator.Subject, project: pr.Project}
			known = true
		}
	}
	if !known {
		return false
	}
	return authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "read",
		Resource: authz.Resource{Type: "process", ID: p.id, Name: p.methodology, Org: p.org, Owner: p.owner, ProjectID: p.project}}) == nil
}
