package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A read query over a view (ADR 0160): anchored patterns grown a hop at a time, WHERE, RETURN; the view decides what
// it sees (the drafts of a change on its flow), the caps stop it.
func TestQueryView(t *testing.T) { forEachRepo(t, testQueryView) }

func testQueryView(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	main := View{Namespace: f.req.Namespace, Branch: domain.MainBranch}
	run := func(v View, q string) QueryResult {
		t.Helper()
		res, err := g.QueryView(ctx, v, q, QueryOptions{})
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return res
	}
	rows := func(res QueryResult) string {
		var out []string
		for _, r := range res.Rows {
			out = append(out, fmt.Sprint(r...))
		}
		return strings.Join(out, ";")
	}

	res := run(main, "MATCH (t:TestCase)-[:verifies]->(r:Requirement)-[s:satisfies]->(n:Need) RETURN t.key, r.title, type(s), n.key")
	if got := rows(res); got != "TST-1Use PSP v1satisfiesNEED-1" {
		t.Fatalf("chain: %q", got)
	}
	// backwards from a key, the WHERE on a property, the path and its nodes and links
	res = run(main, "MATCH p = (n {key: 'NEED-1'})<-[*1..2]-(x) WHERE x.key STARTS WITH 'TST' RETURN p, length(p) AS hops")
	if len(res.Rows) != 1 || res.Rows[0][1] != int64(2) || len(res.Nodes) != 3 || len(res.Links) != 2 {
		t.Fatalf("path: %+v", res)
	}
	path := res.Rows[0][0].(map[string]any)["$path"].(map[string]any)
	if nodes := path["nodes"].([]any); nodes[0] != string(f.need.ID) || nodes[2] != string(f.test.ID) {
		t.Fatalf("path order: %v", path)
	}
	// the wrong direction matches nothing; either direction matches both
	if res = run(main, "MATCH (n:Need)-[:satisfies]->(r) RETURN r"); len(res.Rows) != 0 {
		t.Fatalf("direction: %+v", res.Rows)
	}
	if res = run(main, "MATCH (r:Requirement)--(x) RETURN x.key ORDER BY x.key"); rows(res) != "NEED-1;TST-1" {
		t.Fatalf("either: %q", rows(res))
	}
	// aggregates
	if res = run(main, "MATCH (r:Requirement)--(x) RETURN r.key, count(x) AS n, collect(x.key) AS keys"); len(res.Rows) != 1 || res.Rows[0][1] != int64(2) {
		t.Fatalf("aggregate: %+v", res.Rows)
	}
	// a change view sees its drafts: a test created and linked in the change only
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "more tests", ForkCommit: f.base.ID}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "TST-2", Type: "TestCase", Rationale: "cover"}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{AddLinks: []LinkWrite{{Type: "verifies", To: f.req.Ref()}}}))
	q := "MATCH (r {key: 'REQ-1'})<-[:verifies]-(t) RETURN t.key ORDER BY t.key"
	if res = run(View{Change: c.ID}, q); rows(res) != "TST-1;TST-2" {
		t.Fatalf("change view: %q", rows(res))
	}
	if res = run(main, q); rows(res) != "TST-1" {
		t.Fatalf("main beside the change: %q", rows(res))
	}
	// a second pattern joins on a variable; a caller who may not read a node does not reach it
	res, err := g.QueryView(ctx, main, "MATCH (t:TestCase)-->(r), (r)-->(n) RETURN n.key", QueryOptions{Visible: func(n domain.Node) bool { return n.Key != "NEED-1" }})
	if err != nil || len(res.Rows) != 0 {
		t.Fatalf("visible: %+v %v", res.Rows, err)
	}
	// OPTIONAL MATCH keeps the rows it matches nothing for, its variables null; its WHERE is its own
	if res = run(main, "MATCH (r:Requirement) OPTIONAL MATCH (r)<-[:verifies]-(t) RETURN r.key, t.key"); rows(res) != "REQ-1TST-1" {
		t.Fatalf("optional: %q", rows(res))
	}
	if res = run(main, "MATCH (n:Need) OPTIONAL MATCH (n)<-[:verifies]-(t) RETURN n.key, t.key, t IS NULL AS none"); rows(res) != "NEED-1<nil> true" {
		t.Fatalf("optional null: %q", rows(res))
	}
	if res = run(main, "MATCH (r:Requirement) OPTIONAL MATCH (r)<-[:verifies]-(t) WHERE t.key = 'TST-9' RETURN r.key, t"); rows(res) != "REQ-1<nil>" {
		t.Fatalf("optional where: %q", rows(res))
	}
	if res = run(main, "MATCH (n:Need) OPTIONAL MATCH (n)<-[:verifies]-(t) MATCH (t)-->(x) RETURN x"); len(res.Rows) != 0 {
		t.Fatalf("after a null: %+v", res.Rows)
	}
	// EXISTS and pattern predicates: in WHERE, negated, in RETURN
	for q, want := range map[string]string{
		"MATCH (r:Requirement) WHERE EXISTS { (r)<-[:verifies]-(:TestCase) } RETURN r.key":                    "REQ-1",
		"MATCH (r:Requirement) WHERE NOT (r)<-[:verifies]-() RETURN r.key":                                    "",
		"MATCH (n:Need) WHERE NOT EXISTS { (n)<-[:verifies]-(t) WHERE t.key STARTS WITH 'TST' } RETURN n.key": "NEED-1",
		"MATCH (r:Requirement) RETURN r.key, EXISTS { (r)-[:satisfies]->(n:Need) } AS covered":                "REQ-1true",
		"MATCH (n:Need) RETURN n.key, (n)-->() AS out ORDER BY out":                                           "NEED-1false",
	} {
		if res = run(main, q); rows(res) != want {
			t.Fatalf("%s: %q", q, rows(res))
		}
	}
	// a node bound before the match: the node a person queries from
	res, err = g.QueryView(ctx, main, "MATCH (n)-[l]-(m) RETURN type(l), m.key ORDER BY m.key", QueryOptions{Bind: map[string]domain.NodeID{"n": f.req.ID}})
	if err != nil || rows(res) != "satisfiesNEED-1;verifiesTST-1" {
		t.Fatalf("bind: %+v %v", res.Rows, err)
	}
	// a node the caller may not read is not in the graph it sees: NOT EXISTS and OPTIONAL MATCH do not meet it either
	hide := QueryOptions{Visible: func(n domain.Node) bool { return n.Key != "TST-1" }}
	res, err = g.QueryView(ctx, main, "MATCH (r:Requirement) WHERE NOT (r)<-[:verifies]-() OPTIONAL MATCH (r)--(x) RETURN r.key, x.key", hide)
	if err != nil || rows(res) != "REQ-1NEED-1" {
		t.Fatalf("hidden: %+v %v", res.Rows, err)
	}
	// caps: the reads stop the match, and say so
	res, err = g.QueryView(ctx, main, "MATCH (t:TestCase)-[*1..3]-(x) RETURN x", QueryOptions{MaxReads: 2})
	if err != nil || !res.Truncated || len(res.Notes) == 0 {
		t.Fatalf("cap: %+v %v", res, err)
	}
	// refused: no anchor, a write, an unknown variable, a view missing
	for _, bad := range []string{"MATCH (n) RETURN n", "MATCH (n:Need) DELETE n", "MATCH (n:Need) RETURN m", "MATCH (a:Need)-[*1..9]-(b) RETURN b",
		"MATCH (n:Need) WHERE EXISTS { (n)--(x) WHERE y.key = 'a' } RETURN n", "MATCH (n:Need) OPTIONAL MATCH (n)--(x) WHERE z.key = 'a' MATCH (z:Need) RETURN n"} {
		if _, err := g.QueryView(ctx, main, bad, QueryOptions{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, err := g.QueryView(ctx, View{}, "MATCH (n:Need) RETURN n", QueryOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no view: %v", err)
	}
}

// TestQueryFrontier: a hop reads the frontier it reaches in one batch, in the direction and the types of its
// relationship: the rows and the reads counted are those of a hop read node by node; a link to itself is read either
// way; a change view sees the links of its drafts entering a node.
func TestQueryFrontier(t *testing.T) { forEachRepo(t, testQueryFrontier) }

func testQueryFrontier(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	tree(t, g, 3, 3, 2)
	if _, err := g.CommitEdits(ctx, Edits{Title: "loop", By: "tester", ProjectID: "PROJ-ROOT", Edits: []NodeEdit{
		{Key: "S", Type: "Item", Links: []LinkEdit{{Type: "loop", ToKey: "S"}, {Type: "ref", ToKey: "P0"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	head := must[domain.Commit](t)(g.BranchHead(ctx, "", domain.MainBranch))
	main := View{Namespace: head.Namespace, Branch: domain.MainBranch}
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "more", ForkCommit: head.ID}))
	imp := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: "R.0", Rationale: "link"}))
	s := must[domain.Node](t)(g.NodeByKey(ctx, "", "S"))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, imp.ID, edit{AddLinks: []LinkWrite{{Type: "child", To: s.Ref()}}}))
	change := View{Change: c.ID}
	for _, tc := range []struct {
		v       View
		q, want string
	}{
		{main, "MATCH (r:Item {key: 'R'})-[:child]->(a)-[:child]->(b)-[:child]->(x) RETURN count(x)", "27"},
		{change, "MATCH (r:Item {key: 'R'})-[:child]->(a)-[:child]->(b)-[:child]->(x) RETURN count(x)", "27"},
		{main, "MATCH (r:Item {key: 'R'})-[:child*1..3]->(x) RETURN count(x)", "39"},
		{change, "MATCH (r:Item {key: 'R'})-[:child*1..3]-(x) RETURN count(x)", "40"},
		{main, "MATCH (x:Item)<-[:child]-(a)<-[:child]-(r) WHERE r.key = 'R' RETURN count(x)", "9"},
		{main, "MATCH (p:Pool {key: 'P0'})<-[:ref]-(a)-[:child]->(b) RETURN count(b)", "39"},
		{main, "MATCH (a:Item)-->(b) WHERE a.key STARTS WITH 'R.1' RETURN count(b)", "20"},
		{main, "MATCH (s {key: 'S'})-[:loop]->(x) RETURN x.key", "S"},
		{main, "MATCH (s {key: 'S'})<-[:loop]-(x) RETURN x.key", "S"},
		{main, "MATCH (s {key: 'S'})<-[:child]-(x) RETURN x.key", ""},
		{change, "MATCH (s {key: 'S'})<-[:child]-(x) RETURN x.key", "R.0"},
		{change, "MATCH (r {key: 'R'})-[:child]->(a)-[:child]->(s {key: 'S'}) RETURN a.key", "R.0"},
	} {
		query := func(batch int) (string, QueryResult) {
			t.Helper()
			queryHopBatch = batch
			defer func() { queryHopBatch = 200 }()
			res, err := g.QueryView(ctx, tc.v, tc.q, QueryOptions{})
			if err != nil {
				t.Fatalf("%s: %v", tc.q, err)
			}
			var got []string
			for _, r := range res.Rows {
				got = append(got, fmt.Sprint(r...))
			}
			return strings.Join(got, ";"), res
		}
		got, res := query(200)
		one, by := query(1)
		if got != tc.want || one != tc.want || res.Reads != by.Reads || res.Truncated || by.Truncated {
			t.Fatalf("%v %s: %q in %d reads, node by node %q in %d, want %q", tc.v, tc.q, got, res.Reads, one, by.Reads, tc.want)
		}
	}
}
