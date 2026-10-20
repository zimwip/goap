package graphsvc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/graph/graph"
)

// The statistics of the graph (the graph statistics view of the web): read like the namespaces, aggregates only.

// GetGraphOverview serves graph.Graph.GraphOverview: what every namespace holds.
func (h *Handler) GetGraphOverview(ctx context.Context, _ *connect.Request[graphv1.GetGraphOverviewRequest]) (*connect.Response[graphv1.GetGraphOverviewResponse], error) {
	ns, err := h.Graph.GraphOverview(ctx)
	out := &graphv1.GetGraphOverviewResponse{}
	for _, n := range ns {
		out.Namespaces = append(out.Namespaces, &graphv1.NamespaceSummary{Namespace: n.Namespace, Nodes: int32(n.Nodes), Versions: int32(n.Versions),
			LinkRows: int32(n.LinkRows), Changes: int32(n.Changes), Baselines: int32(n.Baselines), Branches: int32(n.Branches), Types: int32(n.Types),
			LogEntries: int32(n.LogItems)})
	}
	return res(out, err)
}

// GetGraphStats serves graph.Graph.GraphStats: the statistics of a namespace at the head of a lasting branch.
func (h *Handler) GetGraphStats(ctx context.Context, r *connect.Request[graphv1.GetGraphStatsRequest]) (*connect.Response[graphv1.GetGraphStatsResponse], error) {
	s, err := h.Graph.GraphStats(ctx, r.Msg.Namespace, r.Msg.Branch)
	if err != nil {
		return res[graphv1.GetGraphStatsResponse](nil, err)
	}
	out := &graphv1.GraphStats{Namespace: s.Namespace, Branch: s.Branch, BaselineId: string(s.Baseline.ID),
		Branches: &graphv1.BranchStats{Total: int32(s.Branches.Total), Lasting: int32(s.Branches.Lasting), Ephemeral: int32(s.Branches.Ephemeral),
			ByStatus: counts32(s.Branches.ByStatus), Names: s.Branches.Names},
		State: stateStatsToPB(s.State)}
	if s.Baseline.ID != "" {
		out.BaselineAt = timestamppb.New(s.Baseline.CreatedAt)
	}
	for _, m := range graph.StoreMeasures {
		out.Store = append(out.Store, &graphv1.StoreMeasure{Name: m, Counts: counts32(s.Store[m])})
	}
	return res(&graphv1.GetGraphStatsResponse{Stats: out}, nil)
}

func stateStatsToPB(s graph.StateStats) *graphv1.StateStats {
	out := &graphv1.StateStats{Nodes: int32(s.Nodes), Deleted: int32(s.Deleted), Links: int32(s.Links), Suspect: int32(s.Suspect),
		External: int32(s.External), Isolated: int32(s.Isolated), SelfLinks: int32(s.SelfLinks), Reciprocal: int32(s.Reciprocal),
		Density: s.Density, ComponentCount: int32(s.ComponentCount), Out: distributionToPB(s.Out), In: distributionToPB(s.In),
		Degree: distributionToPB(s.Degree), Versions: distributionToPB(s.Versions), Properties: distributionToPB(s.Properties),
		Owners: int32(s.Owners), Projects: int32(s.Projects)}
	for _, c := range s.Components {
		out.Components = append(out.Components, int32(c))
	}
	for _, b := range s.Histogram {
		out.Histogram = append(out.Histogram, &graphv1.HistogramBucket{Label: b.Label, From: int32(b.From), To: int32(b.To), Count: int32(b.Count)})
	}
	for _, t := range s.Types {
		out.Types = append(out.Types, &graphv1.NodeTypeStats{Type: t.Type, Nodes: int32(t.Nodes), States: counts32(t.States), Out: distributionToPB(t.Out),
			In: distributionToPB(t.In), Versions: distributionToPB(t.Versions), Properties: distributionToPB(t.Properties), Isolated: int32(t.Isolated)})
	}
	for _, lt := range s.LinkTypes {
		pb := &graphv1.LinkTypeStats{Type: lt.Type, Links: int32(lt.Links), Suspect: int32(lt.Suspect), Sources: int32(lt.Sources),
			Targets: int32(lt.Targets), MaxOut: int32(lt.MaxOut)}
		for _, p := range lt.Pairs {
			pb.Pairs = append(pb.Pairs, &graphv1.TypePair{From: p.From, To: p.To, Count: int32(p.Count)})
		}
		out.LinkTypes = append(out.LinkTypes, pb)
	}
	for _, hub := range s.Hubs {
		out.Hubs = append(out.Hubs, &graphv1.HubNode{Id: string(hub.ID), Key: hub.Key, Type: hub.Type, Out: int32(hub.Out), In: int32(hub.In)})
	}
	return out
}

func distributionToPB(d graph.Distribution) *graphv1.Distribution {
	return &graphv1.Distribution{Min: int32(d.Min), Max: int32(d.Max), Mean: d.Mean, Median: d.Median, P90: int32(d.P90), Sum: int32(d.Sum)}
}

func counts32(m map[string]int) map[string]int32 {
	out := make(map[string]int32, len(m))
	for k, v := range m {
		out[k] = int32(v)
	}
	return out
}
