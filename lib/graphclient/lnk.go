package graphclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/rpcerr"
)

// Federation is the resolver of the sources of kind lnk.KindGraph (ADR 0154): a namespace of another graph service,
// Source.Location its base URL. It reads a node by its key (GetNode), so a graph federates another with no RPC of its
// own; the node it answers carries the name of the source it was read through (domain.Node.Source).
type Federation struct {
	// HTTP is the client of the requests (nil: http.DefaultClient); Options are added to every client.
	HTTP    *http.Client
	Options []connect.ClientOption

	mu      sync.Mutex
	clients map[string]*Client
}

func (f *Federation) client(location string) *Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clients == nil {
		f.clients = map[string]*Client{}
	}
	c, ok := f.clients[location]
	if !ok {
		hc := f.HTTP
		if hc == nil {
			hc = http.DefaultClient
		}
		c = NewClient(hc, strings.TrimSuffix(location, "/"), f.Options...)
		f.clients[location] = c
	}
	return c
}

// Resolve reads the node a URL names in the graph of the source.
func (f *Federation) Resolve(ctx context.Context, src lnk.Source, u lnk.URL) (lnk.Target, error) {
	n, err := f.client(src.Location).nodeByKey(ctx, src.NamespaceOf(), u.Key, u.Version)
	if errors.Is(err, graphapi.ErrNotFound) {
		return lnk.Target{}, fmt.Errorf("%s: %w", u, lnk.ErrNotFound)
	}
	if err != nil {
		return lnk.Target{}, fmt.Errorf("%s: %w", u, err)
	}
	if n.Deleted || lnk.BareType(n.Type) != u.Type {
		return lnk.Target{}, fmt.Errorf("%s: %w", u, lnk.ErrNotFound)
	}
	n.Source = u.Source
	t := n.Target()
	t.URL = t.URL.At(u.Version)
	return t, nil
}

// URLOf gives the URL of a node of the source: its type and key (a node id of another graph means nothing here).
func (f *Federation) URLOf(_ context.Context, src lnk.Source, t lnk.Target) (lnk.URL, error) {
	if t.Key == "" || t.Type == "" {
		return lnk.URL{}, fmt.Errorf("source %s: a URL of another graph needs a type and a key: %w", src.Name, lnk.ErrInvalid)
	}
	return lnk.New(src.Name, lnk.BareType(t.Type), t.Key), nil
}

// nodeByKey reads the latest version of a node by its key, or the version v of it.
func (c *Client) nodeByKey(ctx context.Context, namespace, key string, v int) (domain.Node, error) {
	res, err := c.rpc.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Namespace: namespace, Key: key}))
	if err != nil {
		return domain.Node{}, rpcerr.FromConnect(err)
	}
	n := pbconv.NodeFromPB(res.Msg.GetView().GetNode())
	if v == 0 || int(n.Version) == v {
		return n, nil
	}
	res, err = c.rpc.GetNode(ctx, connect.NewRequest(&graphv1.GetNodeRequest{Ref: &graphv1.NodeRef{Id: string(n.ID), Version: int32(v)}}))
	if err != nil {
		return domain.Node{}, rpcerr.FromConnect(err)
	}
	return pbconv.NodeFromPB(res.Msg.GetView().GetNode()), nil
}
