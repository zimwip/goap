package cypher

import (
	"strings"
	"testing"
)

func TestParseSubset(t *testing.T) {
	q, err := Parse("MATCH p = (r:Requirement {key: 'R-1'})-[s:satisfies|refines*1..3]->(n:`alm@Need`), (r)<--(x) WHERE n.title CONTAINS 'pay' AND NOT s IS NULL AND 1 < 2 <= 3 RETURN DISTINCT r.title AS title, count(*), n ORDER BY title DESC SKIP 1 LIMIT 10")
	if err != nil {
		t.Fatal(err)
	}
	m := q.Matches[0]
	if len(m.Patterns) != 2 || m.Patterns[0].Var != "p" || len(m.Patterns[0].Nodes) != 2 {
		t.Fatalf("patterns %+v", m.Patterns)
	}
	r := m.Patterns[0].Rels[0]
	if r.Var != "s" || r.Dir != Right || len(r.Types) != 2 || r.Range == nil || r.Range.Min != 1 || r.Range.Max != 3 {
		t.Fatalf("rel %+v", r)
	}
	if got := m.Patterns[0].Nodes[1].Labels; len(got) != 1 || got[0] != "alm@Need" {
		t.Fatalf("labels %v", got)
	}
	if m.Patterns[1].Rels[0].Dir != Left {
		t.Fatalf("left %+v", m.Patterns[1].Rels[0])
	}
	if len(Conjuncts(m.Where)) != 4 {
		t.Fatalf("where %#v", m.Where)
	}
	ret := q.Return
	if !ret.Distinct || len(ret.Items) != 3 || ret.Items[0].Name != "title" || ret.Items[1].Name != "count(*)" || !ret.Order[0].Desc || ret.Limit == nil || ret.Skip == nil {
		t.Fatalf("return %+v", ret)
	}
}

func TestParseRanges(t *testing.T) {
	for src, want := range map[string]Range{"*": {1, 0}, "*2": {2, 2}, "*2..": {2, 0}, "*..4": {1, 4}, "*0..2": {0, 2}} {
		q, err := Parse("MATCH (a)-[" + src + "]-(b) RETURN a")
		if err != nil {
			t.Fatal(src, err)
		}
		if got := *q.Matches[0].Patterns[0].Rels[0].Range; got != want {
			t.Errorf("%s: %+v, want %+v", src, got, want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, src := range []string{
		"CREATE (n) RETURN n",
		"MATCH (n) SET n.x = 1",
		"MATCH (n) DETACH DELETE n",
		"MATCH (n) WITH n RETURN n",
		"UNWIND [1] AS x MATCH (n) RETURN n",
		"OPTIONAL MATCH (n) RETURN n",
		"MATCH (n) RETURN n UNION MATCH (m) RETURN m",
		"MATCH (n {key: $k}) RETURN n",
		"MATCH (n) RETURN [x IN [1] | x]",
		"MATCH (n) RETURN apoc.x(n)",
		"MATCH (n) RETURN nosuch(n)",
		"CALL db.labels()",
		"RETURN 1",
		"MATCH (n RETURN n",
		"",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
}

func TestEval(t *testing.T) {
	s := MapScope{"x": 3.0, "s": "Payment", "l": []any{1.0, 2.0}, "n": nil}
	for src, want := range map[string]any{
		"x + 1 = 4":                             true,
		"x > 2 AND s STARTS WITH 'Pay'":         true,
		"n = 1":                                 nil,
		"n IS NULL":                             true,
		"NOT (n = 1)":                           nil,
		"false AND n = 1":                       false,
		"2 IN l":                                true,
		"toLower(s) + '!'":                      "payment!",
		"size(l) * 2":                           4.0,
		"coalesce(n, 'd')":                      "d",
		"CASE WHEN x > 5 THEN 'a' ELSE 'b' END": "b",
		"l[-1]":                                 2.0,
		"'a\\'b'":                               "a'b",
	} {
		q, err := Parse("MATCH (a) RETURN " + src)
		if err != nil {
			t.Fatal(src, err)
		}
		got, err := Eval(q.Return.Items[0].Expr, s)
		if err != nil {
			t.Fatal(src, err)
		}
		if !Equal(got, want) && !(got == nil && want == nil) {
			t.Errorf("%s = %#v, want %#v", src, got, want)
		}
	}
}

func TestErrorPosition(t *testing.T) {
	_, err := Parse("MATCH (n)\nCREATE (m)\nRETURN n")
	if err == nil || !strings.HasPrefix(err.Error(), "2:1:") {
		t.Fatalf("error %v", err)
	}
}

func TestProject(t *testing.T) {
	rows := []MapScope{{"a": "x", "b": 1.0}, {"a": "y", "b": 2.0}, {"a": "x", "b": 3.0}}
	q, err := Parse("MATCH (a) RETURN a, count(*) AS n, collect(b) AS bs, sum(b) ORDER BY n DESC, a LIMIT 5")
	if err != nil {
		t.Fatal(err)
	}
	cols, out, err := Project(q.Return, nil, rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cols, ",") != "a,n,bs,sum(b)" || len(out) != 2 || out[0][0] != "x" || out[0][1] != 2.0 || out[0][3] != 4.0 || out[1][0] != "y" {
		t.Fatalf("%v %v", cols, out)
	}
	q, _ = Parse("MATCH (a) RETURN DISTINCT a ORDER BY a DESC SKIP 1")
	_, out, err = Project(q.Return, nil, rows)
	if err != nil || len(out) != 1 || out[0][0] != "x" {
		t.Fatalf("%v %v", out, err)
	}
	q, _ = Parse("MATCH (a) RETURN count(*)")
	_, out, _ = Project(q.Return, nil, nil)
	if len(out) != 1 || out[0][0] != 0.0 {
		t.Fatalf("empty count %v", out)
	}
	q, _ = Parse("MATCH (a) RETURN *")
	cols, out, _ = Project(q.Return, []string{"a", "b"}, rows[:1])
	if len(cols) != 2 || out[0][1] != 1.0 {
		t.Fatalf("star %v %v", cols, out)
	}
	if n, ok := Streams(q.Return); !ok || n != -1 {
		t.Fatalf("streams %v %v", n, ok)
	}
}
