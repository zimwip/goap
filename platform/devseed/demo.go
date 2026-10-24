// Package devseed holds the data of the development and demo compositions: the ALM demo repository and the local file
// system document repository. It is separate from the platform bootstrap (graphsvc.Boot), which every composition runs.
// The demo is loaded on demand (the "Load demo data" setting, RPC LoadDemoData) or at start when asked
// (GOAP_GRAPH_SEED=demo for the graph service, GOAP_DEV_DEMO=1 for goap-dev); the document repository is seeded by
// goap-dev at every start.
package devseed

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
)

// alm is the namespace of the delivery domain (domains/alm.yaml).
const alm = "alm"

// DemoProject is the project of the demo: the ALM data acts in it, and the demo organisation (ORG-ACME) holds the roles
// of its methodology there.
const DemoProject = "PROJ-DEMO"

// demoJSON is the demo itself, shared with the Rust graph service (ADR 0163), which loads the same data.
//
//go:embed demo.json
var demoJSON []byte

// demoData is the demo: the project (DemoProject) and the assignment binding the organisation to it, the units of the
// organisation, the owners of the nodes, and the ALM nodes and their links.
type demoData struct {
	Project struct {
		Key, Name, Description, Kind, Status string
		Methodologies                        []string
	}
	Assignment struct {
		Roles       []string
		Description string
	}
	Units []struct {
		Key, Name, Kind, Parent string
	}
	Owners map[string]string
	Nodes  []demoNode
	Links  [][3]string
}

// demoNode is a node of the demo repository.
type demoNode struct {
	Key, Type  string
	Properties map[string]any
	// State is the state a node of a lifecycle commits in: a node is created in the initial state, which no
	// change leaves a node in if it is not committable.
	State string
}

func demo() (demoData, error) {
	var d demoData
	if err := json.Unmarshal(demoJSON, &d); err != nil {
		return d, fmt.Errorf("demo data: %w", err)
	}
	if d.Project.Key != DemoProject {
		return d, fmt.Errorf("demo data: the project is %s, not %s", d.Project.Key, DemoProject)
	}
	return d, nil
}

// Demo loads a small ALM repository (namespace alm) once: needs, requirements and tests
// (methodologies/examples/impact-analysis.yaml), and the functions, components, build artifacts, applications,
// solution, data, interfaces and flows of methodologies/sdlc.yaml, with a small organisation owning them and the demo
// project (DemoProject) bound to that organisation by an assignment. Each part is seeded when it is missing, so a
// graph holding an older demo gains the project. It reports whether it seeded anything. The graph is bootstrapped
// first (a seed is an ordinary change, ADR 0054).
func Demo(ctx context.Context, g *graph.Graph) (bool, error) {
	if err := g.Bootstrap(ctx); err != nil {
		return false, err
	}
	d, err := demo()
	if err != nil {
		return false, err
	}
	seeded, err := demoOrganisation(ctx, g, d)
	if err != nil {
		return false, err
	}
	if _, err := g.NodeByKey(ctx, alm, "NEED-1"); err == nil {
		return seeded, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	if err := demoRepository(ctx, g, d); err != nil {
		return false, err
	}
	return true, nil
}

// DemoLoader is Demo as the graph handler takes it (graphsvc.Handler.Demo, the "Load demo data" setting): whether it
// wrote anything, and the key of the demo project. Once something is written the directory (when given) reads the
// organisation again, so the demo project is offered at once.
func DemoLoader(d *access.Directory) func(context.Context, *graph.Graph) (bool, string, error) {
	return func(ctx context.Context, g *graph.Graph) (bool, string, error) {
		loaded, err := Demo(ctx, g)
		if err == nil && loaded && d != nil {
			err = d.Refresh(ctx)
		}
		return loaded, DemoProject, err
	}
}

// exists tells whether the namespace holds a node of that key.
func exists(ctx context.Context, g *graph.Graph, namespace, key string) (bool, error) {
	_, err := g.NodeByKey(ctx, namespace, key)
	if errors.Is(err, graph.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// demoOrganisation seeds the units of the demo organisation under the root unit, and the demo project under the root
// project with the assignment binding the organisation to it, in one change of the organisation namespace (what is
// missing of them).
func demoOrganisation(ctx context.Context, g *graph.Graph, d demoData) (bool, error) {
	hasOrg, err := exists(ctx, g, access.NamespaceOrganisation, "ORG-ACME")
	if err != nil {
		return false, err
	}
	hasProject, err := exists(ctx, g, access.NamespaceOrganisation, DemoProject)
	if err != nil {
		return false, err
	}
	if hasOrg && hasProject {
		return false, nil
	}
	// the organisation first, under the root unit of the bootstrap (ADR 0054): the nodes below are owned by its units.
	// All the units are one change; a unit links to its parent, created by the same change (ToKey) or the root.
	var edits []graph.NodeEdit
	if !hasOrg {
		root, err := g.NodeByKey(ctx, access.NamespaceOrganisation, g.Structure(domain.StructureOrganisation).Root)
		if err != nil {
			return false, err
		}
		for _, u := range d.Units {
			e := graphsvc.SeedNode(u.Key, access.NodeTypeOrgUnit, map[string]any{"name": u.Name, "kind": u.Kind})
			if u.Parent == "" {
				e = linkTo(e, access.LinkPartOf, root.Ref())
			} else {
				e.Links = append(e.Links, graph.LinkEdit{Type: access.LinkPartOf, ToKey: u.Parent})
			}
			edits = append(edits, e)
		}
	}
	if !hasProject {
		// the project hangs under the root project (ADR 0039); the assignment is where it meets the organisation
		root, err := g.NodeByKey(ctx, access.NamespaceOrganisation, g.Structure(domain.StructureProject).Root)
		if err != nil {
			return false, err
		}
		p := d.Project
		project := graphsvc.SeedNode(DemoProject, access.NodeTypeProjectUnit, access.ProjectUnit{Name: p.Name,
			Description: p.Description, Kind: p.Kind, Status: p.Status, Methodologies: p.Methodologies}.Props())
		edits = append(edits, linkTo(project, access.LinkProjectPartOf, root.Ref()))
		asg := graphsvc.SeedNode(access.AssignmentKey("ORG-ACME", DemoProject), access.NodeTypeAssignment, access.Assignment{
			Roles: d.Assignment.Roles, Description: d.Assignment.Description}.Props())
		if hasOrg {
			acme, err := g.NodeByKey(ctx, access.NamespaceOrganisation, "ORG-ACME")
			if err != nil {
				return false, err
			}
			asg = linkTo(asg, access.LinkAssignsOrg, acme.Ref())
		} else {
			asg.Links = append(asg.Links, graph.LinkEdit{Type: access.LinkAssignsOrg, ToKey: "ORG-ACME"})
		}
		asg.Links = append(asg.Links, graph.LinkEdit{Type: access.LinkAssignsProject, ToKey: DemoProject})
		edits = append(edits, asg)
	}
	if err := graphsvc.SeedChange(ctx, g, access.NamespaceOrganisation, "Import demo organisation", edits); err != nil {
		return false, err
	}
	return true, nil
}

// demoRepository seeds the alm data of the demo in the demo project, owned by the units of the demo organisation.
func demoRepository(ctx context.Context, g *graph.Graph, d demoData) error {
	// ownership: a node of one namespace is owned by a unit of the organisation (the owner of its versions)
	nodes, links, owners := d.Nodes, d.Links, d.Owners
	// the nodes and their links are one change (a link belongs to the version of its source, created here)
	edits := make([]graph.NodeEdit, 0, len(nodes))
	for _, n := range nodes {
		e := graphsvc.SeedNode(n.Key, n.Type, n.Properties)
		e.Owner, e.State = owners[n.Key], n.State
		edits = append(edits, e)
	}
	// the nodes of a change link each other, cycles included (an application is composed of components that
	// access data it owns): the links are part of the version of their source
	at := make(map[string]int, len(nodes))
	for i, n := range nodes {
		at[n.Key] = i
	}
	for _, l := range links {
		i, ok := at[l[0]]
		if !ok {
			return fmt.Errorf("demo link from unknown node %s", l[0])
		}
		edits[i].Links = append(edits[i].Links, graph.LinkEdit{Type: alm + "@" + l[1], ToKey: l[2]})
	}
	_, err := g.Commit(ctx, graph.Commit{Namespace: alm, Title: "Import demo data", Intent: "Seed demo data", By: "devseed", BaselineName: "Initial baseline", Edits: edits,
		ProjectID: DemoProject})
	return err
}

func linkTo(e graph.NodeEdit, typ string, to domain.NodeRef) graph.NodeEdit {
	e.Links = append(e.Links, graph.LinkEdit{Type: typ, To: &to})
	return e
}
