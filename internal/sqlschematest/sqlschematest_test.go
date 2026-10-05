package sqlschematest

import (
	"strings"
	"testing"
	"testing/fstest"
)

func load(t *testing.T, files map[string]string, dir string) *Schema {
	t.Helper()
	m := fstest.MapFS{}
	for n, c := range files {
		m[n] = &fstest.MapFile{Data: []byte(c)}
	}
	s, err := Load(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Equivalent spellings of one schema are aligned.
func TestAlignedSpellings(t *testing.T) {
	pg := load(t, map[string]string{"pg/0001.sql": `
CREATE TABLE a (id uuid PRIMARY KEY, n integer[] NOT NULL DEFAULT '{}', ok boolean NOT NULL DEFAULT false, at timestamptz NOT NULL DEFAULT now(), seq bigserial);
CREATE TABLE b (id uuid PRIMARY KEY, a_id uuid NOT NULL REFERENCES a(id) DEFERRABLE INITIALLY DEFERRED, dead boolean NOT NULL DEFAULT false);
ALTER TABLE a ADD FOREIGN KEY (id) REFERENCES b(id) DEFERRABLE INITIALLY DEFERRED;
CREATE UNIQUE INDEX b_live ON b (a_id) WHERE NOT dead;`}, "pg")
	lite := load(t, map[string]string{"s/0001.sql": `
CREATE TABLE a (id text PRIMARY KEY, n text NOT NULL DEFAULT '[]', ok integer NOT NULL DEFAULT 0, at text NOT NULL, seq INTEGER NOT NULL, FOREIGN KEY (id) REFERENCES b(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TABLE b (id text PRIMARY KEY, a_id text NOT NULL REFERENCES a(id) DEFERRABLE INITIALLY DEFERRED, dead integer NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX b_live ON b (a_id) WHERE dead = 0;`}, "s")
	if d := Diff(pg, lite); len(d) > 0 {
		t.Fatalf("spellings should align: %v", d)
	}
}

// Every kind of divergence is reported.
func TestDivergences(t *testing.T) {
	pg := load(t, map[string]string{"pg/0001.sql": `
CREATE TABLE a (id uuid PRIMARY KEY, x text NOT NULL, y text, z text NOT NULL DEFAULT '', only_pg text);
CREATE INDEX a_x ON a (x);
CREATE TABLE gone (id text);`}, "pg")
	lite := load(t, map[string]string{"s/0001.sql": `
CREATE TABLE a (id text PRIMARY KEY, x text, y integer, z text NOT NULL DEFAULT 'q');
CREATE INDEX a_x ON a (x, y);
CREATE TABLE extra (id text);`}, "s")
	got := strings.Join(Diff(pg, lite), "\n")
	for _, want := range []string{
		"table extra: only in SQLite", "table gone: only in PostgreSQL", "column a.only_pg: only in PostgreSQL",
		"column a.x: NOT NULL", "column a.y: type", `column a.z: default "''" in PostgreSQL, "'q'" in SQLite`, "index a_x:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A later migration (rename, drop) is applied before the comparison.
func TestMigrationsApplyInOrder(t *testing.T) {
	s := load(t, map[string]string{
		"d/0001.sql": `CREATE TABLE t (id text PRIMARY KEY); CREATE TABLE old (k text); INSERT INTO t VALUES ('x');`,
		"d/0002.sql": `DROP TABLE old; ALTER TABLE t RENAME TO t2;`,
	}, "d")
	if _, ok := s.Tables["t2"]; !ok || len(s.Tables) != 1 {
		t.Fatalf("tables = %v", keys(s.Tables))
	}
}
