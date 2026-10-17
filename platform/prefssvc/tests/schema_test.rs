#[test]
fn schemas_aligned() {
    goap_sqlschematest::assert_aligned("migrations", "migrations_sqlite");
}
