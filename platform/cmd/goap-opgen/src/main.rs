//! Port of `platform/cmd/goap-opgen` (see docs/rust-port.md).
//!
//! Generates the catalogue of operations (`contracts/operations/src/operations_gen.rs`, ADR 0103) from the
//! `goap:operation <code> <kind>` annotations of graph/graph; it fails on a duplicate code or a malformed annotation.
//! `-out` ending in `.go` writes the Go catalogue instead.

use std::process::ExitCode;

fn main() -> ExitCode {
    let mut src = "graph/graph".to_string();
    let mut out = "contracts/operations/src/operations_gen.rs".to_string();
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        let (flag, inline) = match a.split_once('=') {
            Some((f, v)) => (f.to_string(), Some(v.to_string())),
            None => (a.clone(), None),
        };
        let target = match flag.trim_start_matches('-') {
            "src" => &mut src,
            "out" => &mut out,
            _ => {
                eprintln!("goap-opgen: unknown flag {a}\nusage: goap-opgen [-src dir] [-out file]");
                return ExitCode::from(2);
            }
        };
        match inline.or_else(|| args.next()) {
            Some(v) => *target = v,
            None => {
                eprintln!("goap-opgen: flag needs an argument: {a}");
                return ExitCode::from(2);
            }
        }
    }
    match goap_opgen::generate(&src, &out) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("goap-opgen: {e}");
            ExitCode::FAILURE
        }
    }
}
