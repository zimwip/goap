package graphsvc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/lnk"
)

// Two graph spaces (ADR 0154 §9): the platform graph serves the platform, the organisation and the methodologies, and
// is the one graph its callers know; the namespaces of the content live in a content graph it forwards their calls to.
// Router is that forwarding, in front of the graph service of the platform graph: a unary call of the graph service
// naming a namespace another graph holds (a top-level `namespace` field), or a change that lives there (`change_id`,
// or the `id` of a call on a change), is passed as is, headers and body, to that graph; any other call is served here,
// and passed to the other graphs in turn when this one answers not found (a node read by its id). The graphs are the
// declared sources (platform@Source of kind graph with a location and the namespaces it holds, lnk.Source.Holds): the
// source is the driver of the namespaces it holds. The overview of the namespaces and their list are every graph's
// (mergedListings), each namespace naming its source; the other listings are this graph's.

// RouterMaxBody is the largest request body Router reads to route it; a larger one is served here.
const RouterMaxBody = 16 << 20

// Router routes the calls of the graph service between this graph and the graphs holding other namespaces.
type Router struct {
	// Local serves the calls of this graph.
	Local http.Handler
	// Sources are the declared sources (Graph.Sources): the graph sources with a location hold the namespaces they
	// name; a namespace none holds is this graph's. Nil: every call is this graph's.
	Sources func(ctx context.Context) ([]lnk.Source, error)
	// LocalSource is the name of this graph's source in the listings it merges (the platform source).
	LocalSource string
	// Client calls the other graphs (the calls forwarded, and the lookup of a change).
	Client connect.HTTPClient
	// HasChange reports a change this graph holds; nil: none (every change named is looked up in the other graphs).
	HasChange func(ctx context.Context, id domain.ChangeID) (bool, error)

	changes sync.Map // change id -> base URL of the graph holding it ("" for this graph)
}

// headers a forwarded call or a lookup carries over: the caller's identity, its command, its positions in the logs
const forwardedPrefix = "X-Goap-"

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if m := r.method(req.URL.Path); m != nil && mergedListings[m.Name()] && r.Sources != nil && req.Method == http.MethodPost {
		r.merge(w, req, m)
		return
	}
	base, body, named, err := r.route(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if base != "" {
		r.forward(w, req, base, body)
		return
	}
	if body == nil || named {
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		r.Local.ServeHTTP(w, req)
		return
	}
	// a call that names no namespace nor change: this graph, else the first other graph that knows what it reads
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	req.Body = io.NopCloser(bytes.NewReader(body))
	r.Local.ServeHTTP(rec, req)
	if rec.status == http.StatusNotFound {
		for _, base := range r.bases(req.Context()) {
			other := &recorder{header: http.Header{}, status: http.StatusOK}
			r.forward(other, req, base, body)
			if other.status != http.StatusNotFound {
				other.replay(w)
				return
			}
		}
	}
	rec.replay(w)
}

// graphs are the declared graph sources holding namespaces (lnk.Source.Holds), each location once.
func (r *Router) graphs(ctx context.Context) ([]lnk.Source, error) {
	if r.Sources == nil {
		return nil, nil
	}
	all, err := r.Sources(ctx)
	if err != nil {
		return nil, err
	}
	var out []lnk.Source
	for _, src := range all {
		if src.Kind == lnk.KindGraph && src.Location != "" && len(src.Namespaces) > 0 &&
			!slices.ContainsFunc(out, func(o lnk.Source) bool { return o.Location == src.Location }) {
			out = append(out, src)
		}
	}
	return out, nil
}

// bases are the base URLs of the other graphs, each once.
func (r *Router) bases(ctx context.Context) []string {
	gs, _ := r.graphs(ctx)
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Location)
	}
	return out
}

// baseOf is the base URL of the graph holding a namespace, "" for this graph.
func (r *Router) baseOf(ctx context.Context, ns string) (string, error) {
	gs, err := r.graphs(ctx)
	for _, g := range gs {
		if g.Holds(ns) {
			return g.Location, nil
		}
	}
	return "", err
}

// recorder keeps an answer to replay it, or to try another graph instead.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(status int)      { r.status = status }

func (r *recorder) replay(w http.ResponseWriter) {
	for k, vs := range r.header {
		w.Header()[k] = vs
	}
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body.Bytes())
}

// route is the base URL of the graph serving a call ("" for this graph), the body it read (nil: none read) and whether
// the call names a namespace or a change (then its graph is known).
func (r *Router) route(req *http.Request) (base string, body []byte, named bool, err error) {
	if r.Sources == nil || req.Method != http.MethodPost || req.Header.Get("Content-Encoding") != "" {
		return "", nil, false, nil
	}
	ct := req.Header.Get("Content-Type")
	if ct != "application/proto" && ct != "application/json" {
		return "", nil, false, nil // streaming, gRPC: served here
	}
	method := r.method(req.URL.Path)
	if method == nil || method.IsStreamingClient() || method.IsStreamingServer() {
		return "", nil, false, nil
	}
	if body, err = io.ReadAll(io.LimitReader(req.Body, RouterMaxBody+1)); err != nil {
		return "", nil, false, err
	}
	if len(body) > RouterMaxBody {
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), req.Body))
		return "", nil, false, nil
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(method.Input().FullName())
	if err != nil {
		return "", body, true, nil
	}
	msg := mt.New().Interface()
	if ct == "application/json" {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, msg)
	} else {
		err = proto.Unmarshal(body, msg)
	}
	if err != nil {
		return "", body, true, nil // the service answers the malformed request
	}
	fields := msg.ProtoReflect()
	if ns := stringField(fields, "namespace"); ns != "" {
		base, err = r.baseOf(req.Context(), domain.NamespaceOf(ns))
		return base, body, true, err
	}
	id := stringField(fields, "change_id")
	if id == "" && strings.Contains(string(method.Name()), "Change") {
		id = stringField(fields, "id")
	}
	if id == "" {
		return "", body, false, nil
	}
	base, err = r.changeBase(req, domain.ChangeID(id))
	return base, body, true, err
}

// method is the method of the graph service a path names, nil for another service.
func (r *Router) method(path string) protoreflect.MethodDescriptor {
	svc, name, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if !ok || svc != graphv1connect.GraphServiceName {
		return nil
	}
	sd := graphv1.File_goap_graph_v1_graph_proto.Services().ByName("GraphService")
	if sd == nil {
		return nil
	}
	return sd.Methods().ByName(protoreflect.Name(name))
}

func stringField(m protoreflect.Message, name protoreflect.Name) string {
	f := m.Descriptor().Fields().ByName(name)
	if f == nil || f.Kind() != protoreflect.StringKind || f.IsList() || f.IsMap() {
		return ""
	}
	return m.Get(f).String()
}

// changeBase is the graph holding a change: this one when it holds it, else the first of the other graphs that knows
// it, remembered; one no graph knows is this graph's (which answers not found).
func (r *Router) changeBase(req *http.Request, id domain.ChangeID) (string, error) {
	if b, ok := r.changes.Load(id); ok {
		return b.(string), nil
	}
	if r.HasChange != nil {
		ok, err := r.HasChange(req.Context(), id)
		if err != nil {
			return "", err
		}
		if ok {
			r.changes.Store(id, "")
			return "", nil
		}
	}
	for _, base := range r.bases(req.Context()) {
		c := graphv1connect.NewGraphServiceClient(r.Client, base)
		get := connect.NewRequest(&graphv1.GetChangeRequest{Id: string(id)})
		for k, vs := range req.Header {
			if strings.HasPrefix(k, forwardedPrefix) || k == "Authorization" {
				get.Header()[k] = vs
			}
		}
		if _, err := c.GetChange(req.Context(), get); err == nil {
			r.changes.Store(id, base)
			return base, nil
		} else if code := connect.CodeOf(err); code != connect.CodeNotFound && code != connect.CodePermissionDenied {
			return "", errors.New("graph " + base + ": " + err.Error())
		}
	}
	return "", nil
}

// forward passes a call to the graph at base, its headers and body as they came, and its answer back.
func (r *Router) forward(w http.ResponseWriter, req *http.Request, base string, body []byte) {
	out, err := http.NewRequestWithContext(req.Context(), http.MethodPost, strings.TrimSuffix(base, "/")+req.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range req.Header {
		if k != "Content-Length" {
			out.Header[k] = vs
		}
	}
	res, err := r.Client.Do(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

// mergedListings are the calls every graph answers, merged: the namespaces and their overview.
var mergedListings = map[protoreflect.Name]bool{"GetGraphOverview": true, "ListNamespaces": true}

// merge serves a listing every graph answers: this graph's answer, then each graph source's, a namespace listed once
// (this graph first), each summary naming its source. A graph that does not answer is left out of the listing.
func (r *Router) merge(w http.ResponseWriter, req *http.Request, m protoreflect.MethodDescriptor) {
	body, err := io.ReadAll(io.LimitReader(req.Body, RouterMaxBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// the answers are decoded here: none compressed
	req.Header.Del("Accept-Encoding")
	req.Header.Del("Connect-Accept-Encoding")
	ct := req.Header.Get("Content-Type")
	local := &recorder{header: http.Header{}, status: http.StatusOK}
	req.Body = io.NopCloser(bytes.NewReader(body))
	r.Local.ServeHTTP(local, req)
	if local.status != http.StatusOK || (ct != "application/proto" && ct != "application/json") {
		local.replay(w)
		return
	}
	out, err := decodeAnswer(m, ct, local.body.Bytes())
	if err != nil {
		local.replay(w)
		return
	}
	stamp(out, r.LocalSource)
	gs, err := r.graphs(req.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for _, g := range gs {
		other := &recorder{header: http.Header{}, status: http.StatusOK}
		r.forward(other, req, g.Location, body)
		if other.status != http.StatusOK {
			continue
		}
		if msg, err := decodeAnswer(m, ct, other.body.Bytes()); err == nil {
			stamp(msg, g.Name)
			mergeListing(out, msg)
		}
	}
	var b []byte
	if ct == "application/json" {
		b, err = protojson.Marshal(out)
	} else {
		b, err = proto.Marshal(out)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for k, vs := range local.header {
		if k != "Content-Length" && k != "Content-Encoding" {
			w.Header()[k] = vs
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func decodeAnswer(m protoreflect.MethodDescriptor, ct string, b []byte) (proto.Message, error) {
	mt, err := protoregistry.GlobalTypes.FindMessageByName(m.Output().FullName())
	if err != nil {
		return nil, err
	}
	msg := mt.New().Interface()
	if ct == "application/json" {
		return msg, protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, msg)
	}
	return msg, proto.Unmarshal(b, msg)
}

// stamp names the source of the namespaces of an overview that name none.
func stamp(msg proto.Message, source string) {
	if o, ok := msg.(*graphv1.GetGraphOverviewResponse); ok {
		for _, n := range o.Namespaces {
			if n.Source == "" {
				n.Source = source
			}
		}
	}
}

// mergeListing adds to a listing the namespaces of another it does not list yet.
func mergeListing(into, from proto.Message) {
	switch o := into.(type) {
	case *graphv1.GetGraphOverviewResponse:
		for _, n := range from.(*graphv1.GetGraphOverviewResponse).Namespaces {
			if !slices.ContainsFunc(o.Namespaces, func(x *graphv1.NamespaceSummary) bool { return x.Namespace == n.Namespace }) {
				o.Namespaces = append(o.Namespaces, n)
			}
		}
	case *graphv1.ListNamespacesResponse:
		for _, n := range from.(*graphv1.ListNamespacesResponse).Namespaces {
			if !slices.Contains(o.Namespaces, n) {
				o.Namespaces = append(o.Namespaces, n)
			}
		}
		slices.Sort(o.Namespaces)
	}
}
