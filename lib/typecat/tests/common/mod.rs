//! Helpers shared by the tests (included with `mod common;`).

#![allow(dead_code)]

use goap_def::{Domain, parse_domain};

pub fn parse(src: &str) -> Domain {
    parse_domain(src.as_bytes()).unwrap_or_else(|e| panic!("{e}"))
}

pub fn from_file(name: &str) -> Domain {
    let path = format!("{}/../../domains/{name}.yaml", env!("CARGO_MANIFEST_DIR"));
    parse(&std::fs::read_to_string(path).unwrap())
}
