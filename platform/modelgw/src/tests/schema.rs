#[test]
fn test_schemas_aligned() {
    let dir = env!("CARGO_MANIFEST_DIR");
    goap_sqlschematest::assert_aligned(
        &format!("{dir}/migrations"),
        &format!("{dir}/migrations_sqlite"),
    );
}
