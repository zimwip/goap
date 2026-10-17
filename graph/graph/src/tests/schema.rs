//! Port of `schema_test.go`.

// The PostgreSQL and the SQLite schemas of the graph are kept by hand (ADR 0074): they must describe the same tables,
// columns, defaults, keys, constraints and indexes, whatever the spelling of the types.
#[test]
fn schemas_aligned() {
    goap_sqlschematest::assert_aligned("migrations", "migrations_sqlite");
}
