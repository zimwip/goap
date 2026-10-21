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
)

// Two graph spaces (ADR 0154 §9): the platform graph serves the platform, the organisation and the methodologies, and
// is the one graph its callers know; the namespaces of the content live in a content graph it forwards their calls to.
// Router is that forwarding, in front of the graph service of the platform graph: a unary call of the graph service
// naming a namespace another graph holds (a top-level `namespace` field), or a change that lives there (`change_id`,
// or the `id` of a call on a change), is passed as is, headers and body, to that graph; any other call is served here,
// and passed to the other graphs in turn when this one answers not found (a node read by its id). The listings are
// this graph's: the content graph lists its own.

// RouterMaxBody is the largest request body Router reads to route it; a larger one is served here.
const RouterMaxBody = 16 << 20

// Router routes the calls of the graph service between this graph and the graphs holding other namespaces.
type Router struct {
	// Local serves the calls of this graph.
	Local http.Handler
	// Routes gives the base URL of the graph serving a namespace; a namespace it does not name is this graph's.
	Routes map[string]string
	// Client calls the other graphs (the calls forwarded, and the lookup of a change).
	Client connect.HTTPClient
	// HasChange reports a change this graph holds; nil: none (every change named is looked up in the other graphs).
	HasChange func(ctx context.Context, id domain.ChangeID) (bool, error)

	changes sync.Map // change id -> base URL of the graph holding it ("" for this graph)
}

// headers a forwarded call or a lookup carries over: the caller's identity, its command, its positions in the logs
const forwardedPrefix = "X-Goap-"

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
		for _, base := range r.bases() {
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

// bases are the base URLs of the other graphs, each once.
func (r *Router) bases() []string {
	var out []string
	for _, b := range r.Routes {
		if !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	slices.Sort(out)
	return out
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
	if len(r.Routes) == 0 || req.Method != http.MethodPost || req.Header.Get("Content-Encoding") != "" {
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
		return r.Routes[domain.NamespaceOf(ns)], body, true, nil
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
	for _, base := range r.bases() {
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

// ParseRoutes reads the routes of the namespaces other graphs hold: "ns1,ns2=url;ns3=url2" (each group the namespaces a
// graph serves, then its base URL).
func ParseRoutes(spec string) (map[string]string, error) {
	out := map[string]string{}
	for _, group := range strings.Split(spec, ";") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		names, base, ok := strings.Cut(group, "=")
		if !ok || strings.TrimSpace(base) == "" {
			return nil, errors.New("graph routes: want namespaces=url, got " + group)
		}
		for _, ns := range strings.Split(names, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				out[ns] = strings.TrimSpace(base)
			}
		}
	}
	return out, nil
}
