// Package sqlschematest checks that the PostgreSQL and the SQLite migrations of a service describe the same schema
// (ADR 0074). Both dialects are kept by hand; this guard reads the migrations of each (CREATE TABLE, CREATE INDEX,
// ALTER TABLE ... ADD FOREIGN KEY / RENAME, DROP TABLE, in file order), reduces them to a schema in a common
// vocabulary and reports every difference in tables, columns, nullability, defaults, keys, constraints and indexes.
// It is test-only: no package outside _test files imports it.
//
// What the two dialects spell differently and is not a difference of schema:
//
//   - types: uuid, timestamptz, jsonb, bigserial / bigint / boolean, integer[] are stored in SQLite as text or
//     integers (uuid, timestamptz, jsonb, integer[] -> text; boolean, bigint, bigserial -> integer);
//   - a boolean default (false / true) is 0 / 1, the empty integer array default '{}' is the JSON '[]';
//   - a serial column and an AUTOINCREMENT primary key are "generated"; the generation of a column that is not the
//     primary key is not compared (SQLite only generates a rowid key: the repository assigns such a column);
//   - DEFAULT now() has no SQLite counterpart (the repositories write the timestamp themselves): it is ignored;
//   - a boolean in an index predicate (WHERE NOT superseded) is the test of its integer (superseded = 0);
//   - a foreign key declared inline or by ALTER TABLE is the same constraint.
package sqlschematest

import (
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Schema is the result of a service's migrations.
type Schema struct {
	Tables  map[string]*Table
	Indexes map[string]*Index
}

// Table is a table in the common vocabulary.
type Table struct {
	Name    string
	Columns []*Column
	PK      []string
	Unique  [][]string
	FKs     []string // normalised: "(cols) -> table(cols) [deferred] [on delete x]"
	Checks  []string
}

// Column is a column in the common vocabulary.
type Column struct {
	Name      string
	Type      string // text | integer
	Raw       string // the type as written
	NotNull   bool
	Default   string
	Generated bool
}

// Index is an index in the common vocabulary.
type Index struct {
	Name, Table string
	Cols        string
	Unique      bool
	Where       string
}

// Load reads the migrations of dir (*.sql in name order) of fsys into a Schema.
func Load(fsys fs.FS, dir string) (*Schema, error) {
	names, err := fs.Glob(fsys, dir+"/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("no migration in %s", dir)
	}
	s := &Schema{Tables: map[string]*Table{}, Indexes: map[string]*Index{}}
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		for _, stmt := range statements(string(b)) {
			if err := s.apply(stmt); err != nil {
				return nil, fmt.Errorf("%s: %w\n  %s", n, err, stmt)
			}
		}
	}
	for _, t := range s.Tables {
		t.finish()
	}
	for _, ix := range s.Indexes {
		if t := s.Tables[ix.Table]; t != nil {
			ix.Where = t.boolPredicate(ix.Where)
		}
	}
	return s, nil
}

var (
	lineComment = regexp.MustCompile(`--[^\n]*`)
	spaces      = regexp.MustCompile(`\s+`)
)

// statements splits a migration into its statements (no semicolon occurs inside a string of ours).
func statements(sql string) []string {
	sql = lineComment.ReplaceAllString(sql, "")
	var out []string
	for _, s := range strings.Split(sql, ";") {
		if s = strings.TrimSpace(spaces.ReplaceAllString(s, " ")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var (
	reCreateTable = regexp.MustCompile(`(?is)^CREATE TABLE (?:IF NOT EXISTS )?(\w+) \((.*)\)$`)
	reCreateIndex = regexp.MustCompile(`(?is)^CREATE (UNIQUE )?INDEX (?:IF NOT EXISTS )?(\w+) ON (\w+) ?\(([^)]*)\)(?: WHERE (.*))?$`)
	reDropTable   = regexp.MustCompile(`(?is)^DROP TABLE (?:IF EXISTS )?(\w+)$`)
	reDropIndex   = regexp.MustCompile(`(?is)^DROP INDEX (?:IF EXISTS )?(\w+)$`)
	reRename      = regexp.MustCompile(`(?is)^ALTER TABLE (\w+) RENAME TO (\w+)$`)
	reAddFK       = regexp.MustCompile(`(?is)^ALTER TABLE (\w+) ADD (FOREIGN KEY .*)$`)
	reAddColumn   = regexp.MustCompile(`(?is)^ALTER TABLE (\w+) ADD (?:COLUMN )?(\w+ .*)$`)
	reDropColumn  = regexp.MustCompile(`(?is)^ALTER TABLE (\w+) DROP (?:COLUMN )?(\w+)$`)
)

func (s *Schema) apply(stmt string) error {
	up := strings.ToUpper(stmt)
	switch {
	case strings.HasPrefix(up, "INSERT "), strings.HasPrefix(up, "UPDATE "), strings.HasPrefix(up, "DELETE "):
		return nil // data, not schema
	}
	if m := reCreateTable.FindStringSubmatch(stmt); m != nil {
		t, err := parseTable(m[1], m[2])
		if err != nil {
			return err
		}
		s.Tables[m[1]] = t
		return nil
	}
	if m := reCreateIndex.FindStringSubmatch(stmt); m != nil {
		s.Indexes[m[2]] = &Index{Name: m[2], Table: m[3], Cols: strings.ReplaceAll(strings.TrimSpace(m[4]), " ,", ","), Unique: m[1] != "", Where: strings.TrimSpace(m[5])}
		return nil
	}
	if m := reDropTable.FindStringSubmatch(stmt); m != nil {
		delete(s.Tables, m[1])
		for n, ix := range s.Indexes {
			if ix.Table == m[1] {
				delete(s.Indexes, n)
			}
		}
		return nil
	}
	if m := reDropIndex.FindStringSubmatch(stmt); m != nil {
		delete(s.Indexes, m[1])
		return nil
	}
	if m := reRename.FindStringSubmatch(stmt); m != nil {
		t := s.Tables[m[1]]
		if t == nil {
			return fmt.Errorf("rename of an unknown table %s", m[1])
		}
		delete(s.Tables, m[1])
		t.Name = m[2]
		s.Tables[m[2]] = t
		for _, ix := range s.Indexes {
			if ix.Table == m[1] {
				ix.Table = m[2]
			}
		}
		return nil
	}
	if m := reAddFK.FindStringSubmatch(stmt); m != nil {
		t := s.Tables[m[1]]
		if t == nil {
			return fmt.Errorf("ALTER of an unknown table %s", m[1])
		}
		return t.addConstraint(m[2])
	}
	if m := reDropColumn.FindStringSubmatch(stmt); m != nil {
		t := s.Tables[m[1]]
		if t == nil {
			return fmt.Errorf("ALTER of an unknown table %s", m[1])
		}
		for i, c := range t.Columns {
			if c.Name == m[2] {
				t.Columns = append(t.Columns[:i], t.Columns[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("unknown column %s.%s", m[1], m[2])
	}
	if m := reAddColumn.FindStringSubmatch(stmt); m != nil {
		t := s.Tables[m[1]]
		if t == nil {
			return fmt.Errorf("ALTER of an unknown table %s", m[1])
		}
		return t.addItem(m[2])
	}
	return fmt.Errorf("statement not understood by sqlschematest")
}

// splitTop splits s at the commas outside parentheses.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

func parseTable(name, body string) (*Table, error) {
	t := &Table{Name: name}
	for _, item := range splitTop(body) {
		if err := t.addItem(item); err != nil {
			return nil, fmt.Errorf("table %s: %w", name, err)
		}
	}
	return t, nil
}

var constraintStart = regexp.MustCompile(`(?i)^(PRIMARY KEY|UNIQUE|FOREIGN KEY|CHECK|CONSTRAINT)\b`)

func (t *Table) addItem(item string) error {
	if constraintStart.MatchString(item) {
		return t.addConstraint(item)
	}
	return t.addColumn(item)
}

var (
	reTablePK     = regexp.MustCompile(`(?is)^PRIMARY KEY ?\(([^)]*)\)$`)
	reTableUnique = regexp.MustCompile(`(?is)^UNIQUE ?\(([^)]*)\)$`)
	reTableFK     = regexp.MustCompile(`(?is)^FOREIGN KEY ?\(([^)]*)\) REFERENCES (\w+) ?\(([^)]*)\)(.*)$`)
	reTableCheck  = regexp.MustCompile(`(?is)^CHECK ?\((.*)\)$`)
)

func cols(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(c))
	}
	return out
}

func (t *Table) addConstraint(item string) error {
	switch {
	case reTablePK.MatchString(item):
		t.PK = cols(reTablePK.FindStringSubmatch(item)[1])
	case reTableUnique.MatchString(item):
		t.Unique = append(t.Unique, cols(reTableUnique.FindStringSubmatch(item)[1]))
	case reTableFK.MatchString(item):
		m := reTableFK.FindStringSubmatch(item)
		t.FKs = append(t.FKs, fkText(cols(m[1]), m[2], cols(m[3]), m[4]))
	case reTableCheck.MatchString(item):
		t.Checks = append(t.Checks, normExpr(reTableCheck.FindStringSubmatch(item)[1]))
	default:
		return fmt.Errorf("constraint not understood: %s", item)
	}
	return nil
}

func fkText(from []string, table string, to []string, rest string) string {
	r := strings.ToUpper(strings.TrimSpace(rest))
	s := "(" + strings.Join(from, ", ") + ") -> " + table + "(" + strings.Join(to, ", ") + ")"
	if strings.Contains(r, "DEFERRABLE INITIALLY DEFERRED") {
		s += " deferred"
	}
	if strings.Contains(r, "ON DELETE CASCADE") {
		s += " on delete cascade"
	}
	return s
}

var (
	reColumn     = regexp.MustCompile(`(?is)^(\w+) +([A-Za-z]+(?:\[\])?)(.*)$`)
	reRefInline  = regexp.MustCompile(`(?is)REFERENCES (\w+) ?\((\w+)\)(.*)$`)
	reDefault    = regexp.MustCompile(`(?is)DEFAULT +('(?:[^']|'')*'|\w+\(\)|[-\w.]+)`)
	reCheckInl   = regexp.MustCompile(`(?is)CHECK ?\((.*)\)`)
	reRefTail    = regexp.MustCompile(`(?is) ?(DEFERRABLE INITIALLY DEFERRED|ON DELETE CASCADE)`)
	reStripBlock = regexp.MustCompile(`(?is)(REFERENCES.*|CHECK ?\(.*\))`)
)

func (t *Table) addColumn(item string) error {
	m := reColumn.FindStringSubmatch(item)
	if m == nil {
		return fmt.Errorf("column not understood: %s", item)
	}
	c := &Column{Name: m[1], Raw: m[2]}
	rest := m[3]
	up := strings.ToUpper(rest)
	switch strings.ToLower(c.Raw) {
	case "bigserial", "serial":
		c.Generated, c.NotNull = true, true
	}
	c.Type = baseType(c.Raw)
	if strings.Contains(up, "NOT NULL") {
		c.NotNull = true
	}
	if strings.Contains(up, "AUTOINCREMENT") {
		c.Generated = true
	}
	if strings.Contains(up, "PRIMARY KEY") {
		t.PK = []string{c.Name}
		c.NotNull = true
	}
	if strings.Contains(up, " UNIQUE") {
		t.Unique = append(t.Unique, []string{c.Name})
	}
	if mm := reRefInline.FindStringSubmatch(rest); mm != nil {
		t.FKs = append(t.FKs, fkText([]string{c.Name}, mm[1], []string{mm[2]}, mm[3]))
	}
	if mm := reCheckInl.FindStringSubmatch(rest); mm != nil {
		t.Checks = append(t.Checks, normExpr(mm[1]))
	}
	// the DEFAULT of the column proper, not one inside a CHECK or a REFERENCES clause
	if mm := reDefault.FindStringSubmatch(reStripBlock.ReplaceAllString(rest, "")); mm != nil {
		c.Default = normDefault(c.Raw, mm[1])
	}
	t.Columns = append(t.Columns, c)
	return nil
}

func baseType(raw string) string {
	switch strings.ToLower(raw) {
	case "boolean", "bool", "integer", "int", "bigint", "bigserial", "serial", "smallint":
		return "integer"
	}
	return "text" // text, uuid, timestamptz, jsonb, integer[] ...
}

func normExpr(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// normDefault reduces a default to the SQLite vocabulary; "" is no default.
func normDefault(raw, d string) string {
	d = strings.TrimSpace(d)
	low := strings.ToLower(d)
	switch {
	case low == "now()" || low == "current_timestamp":
		return "" // SQLite has no counterpart, the repositories write the time
	case low == "false":
		return "0"
	case low == "true":
		return "1"
	case strings.EqualFold(raw, "integer[]") && d == "'{}'":
		return "'[]'"
	}
	return d
}

// finish sorts what is a set.
func (t *Table) finish() {
	sort.Strings(t.FKs)
	sort.Strings(t.Checks)
	sort.Slice(t.Unique, func(i, j int) bool { return strings.Join(t.Unique[i], ",") < strings.Join(t.Unique[j], ",") })
}

// boolPredicate rewrites a PostgreSQL boolean test of the table's columns in an index predicate as the SQLite one.
func (t *Table) boolPredicate(where string) string {
	if where == "" {
		return ""
	}
	for _, c := range t.Columns {
		if !strings.EqualFold(c.Raw, "boolean") && !strings.EqualFold(c.Raw, "bool") {
			continue
		}
		where = regexp.MustCompile(`(?i)\bNOT `+c.Name+`\b`).ReplaceAllString(where, c.Name+" = 0")
		where = regexp.MustCompile(`(?i)^`+c.Name+`$`).ReplaceAllString(where, c.Name+" = 1")
	}
	return normExpr(where)
}

// Diff lists the differences between the schemas of the two dialects, as readable lines (none: aligned).
func Diff(pg, lite *Schema) []string {
	var out []string
	add := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	for _, name := range union(keys(pg.Tables), keys(lite.Tables)) {
		p, l := pg.Tables[name], lite.Tables[name]
		switch {
		case p == nil:
			add("table %s: only in SQLite", name)
			continue
		case l == nil:
			add("table %s: only in PostgreSQL", name)
			continue
		}
		pc, lc := columnsByName(p), columnsByName(l)
		for _, cn := range union(keys(pc), keys(lc)) {
			a, b := pc[cn], lc[cn]
			switch {
			case a == nil:
				add("column %s.%s: only in SQLite", name, cn)
			case b == nil:
				add("column %s.%s: only in PostgreSQL", name, cn)
			default:
				if a.Type != b.Type {
					add("column %s.%s: type %s (%s) / %s (%s)", name, cn, a.Type, a.Raw, b.Type, b.Raw)
				}
				if a.NotNull != b.NotNull {
					add("column %s.%s: NOT NULL is %v in PostgreSQL, %v in SQLite", name, cn, a.NotNull, b.NotNull)
				}
				if a.Default != b.Default {
					add("column %s.%s: default %q in PostgreSQL, %q in SQLite", name, cn, a.Default, b.Default)
				}
				if a.Generated != b.Generated && contains(p.PK, cn) {
					add("column %s.%s: generated %v in PostgreSQL, %v in SQLite", name, cn, a.Generated, b.Generated)
				}
			}
		}
		if order(p) != order(l) {
			add("table %s: column order %s (PostgreSQL) / %s (SQLite)", name, order(p), order(l))
		}
		if a, b := strings.Join(p.PK, ","), strings.Join(l.PK, ","); a != b {
			add("table %s: primary key (%s) in PostgreSQL, (%s) in SQLite", name, a, b)
		}
		diffSet(&out, "table "+name+": unique", uniqueTexts(p), uniqueTexts(l))
		diffSet(&out, "table "+name+": foreign key", p.FKs, l.FKs)
		diffSet(&out, "table "+name+": check", p.Checks, l.Checks)
	}
	for _, name := range union(keys(pg.Indexes), keys(lite.Indexes)) {
		p, l := pg.Indexes[name], lite.Indexes[name]
		switch {
		case p == nil:
			add("index %s: only in SQLite", name)
		case l == nil:
			add("index %s: only in PostgreSQL", name)
		case *p != *l:
			add("index %s: %s (%s) %s where %q in PostgreSQL, %s (%s) %s where %q in SQLite", name, p.Table, p.Cols, uniq(p.Unique), p.Where, l.Table, l.Cols, uniq(l.Unique), l.Where)
		}
	}
	return out
}

func uniq(b bool) string {
	if b {
		return "unique"
	}
	return "non-unique"
}

func order(t *Table) string {
	var ns []string
	for _, c := range t.Columns {
		ns = append(ns, c.Name)
	}
	return strings.Join(ns, ",")
}

func uniqueTexts(t *Table) []string {
	var out []string
	for _, u := range t.Unique {
		out = append(out, "("+strings.Join(u, ", ")+")")
	}
	return out
}

func diffSet(out *[]string, what string, pg, lite []string) {
	for _, s := range pg {
		if !contains(lite, s) {
			*out = append(*out, fmt.Sprintf("%s %s: only in PostgreSQL", what, s))
		}
	}
	for _, s := range lite {
		if !contains(pg, s) {
			*out = append(*out, fmt.Sprintf("%s %s: only in SQLite", what, s))
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func columnsByName(t *Table) map[string]*Column {
	m := map[string]*Column{}
	for _, c := range t.Columns {
		m[c.Name] = c
	}
	return m
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range [][]string{a, b} {
		for _, s := range l {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// AssertAligned fails the test when the migrations of pgDir and liteDir (relative to the package under test) describe
// different schemas.
func AssertAligned(t testing.TB, pgDir, liteDir string) {
	t.Helper()
	root := os.DirFS(".")
	pg, err := Load(root, pgDir)
	if err != nil {
		t.Fatalf("PostgreSQL migrations: %v", err)
	}
	lite, err := Load(root, liteDir)
	if err != nil {
		t.Fatalf("SQLite migrations: %v", err)
	}
	if d := Diff(pg, lite); len(d) > 0 {
		t.Errorf("the PostgreSQL (%s) and SQLite (%s) schemas diverge:\n  %s", pgDir, liteDir, strings.Join(d, "\n  "))
	}
}
