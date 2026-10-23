//! Embeds the migrations of the Go services the graph process runs (the same files: one schema, whichever service
//! reads it), in name order.
use std::{env, fmt::Write as _, fs, path::PathBuf};

const SETS: &[(&str, &str)] = &[
    ("GRAPH_PG", "graph/graph/migrations"),
    ("GRAPH_SQLITE", "graph/graph/migrations_sqlite"),
    ("DOMAINS_PG", "graph/domainsvc/migrations"),
    ("DOMAINS_SQLITE", "graph/domainsvc/migrations_sqlite"),
    ("SMARTTAGS_PG", "graph/smarttagsvc/migrations"),
    ("SMARTTAGS_SQLITE", "graph/smarttagsvc/migrations_sqlite"),
];

fn main() {
    let root = PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap()).join("../../..");
    let mut out = String::new();
    for (name, dir) in SETS {
        let dir = root.join(dir);
        println!("cargo:rerun-if-changed={}", dir.display());
        let mut files: Vec<_> = fs::read_dir(&dir)
            .unwrap_or_else(|e| panic!("{}: {e}", dir.display()))
            .filter_map(Result::ok)
            .map(|e| e.path())
            .filter(|p| p.extension().is_some_and(|x| x == "sql"))
            .collect();
        files.sort();
        writeln!(out, "pub static {name}: &[(&str, &str)] = &[").unwrap();
        for f in files {
            let f = f.canonicalize().unwrap();
            let file = f.file_name().unwrap().to_str().unwrap();
            writeln!(out, "    ({file:?}, include_str!({:?})),", f.display().to_string()).unwrap();
        }
        writeln!(out, "];").unwrap();
    }
    fs::write(PathBuf::from(env::var("OUT_DIR").unwrap()).join("migrations.rs"), out).unwrap();
}
