//! Compiles proto/ into the message types (prost), their protobuf JSON forms (pbjson, the forms Connect's JSON
//! codec and the web speak) and, per service, the table of its methods (`services.rs`) the Connect layer routes on.
use std::{env, fmt::Write as _, fs, path::PathBuf};

fn main() {
    let root = PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap()).join("../../proto");
    let mut files: Vec<PathBuf> = Vec::new();
    collect(&root, &mut files);
    files.sort();
    println!("cargo:rerun-if-changed={}", root.display());
    let fds = protox::compile(&files, [&root]).expect("compile proto/");
    let out = PathBuf::from(env::var("OUT_DIR").unwrap());

    let mut cfg = prost_build::Config::new();
    cfg.compile_well_known_types();
    cfg.extern_path(".google.protobuf", "::pbjson_types");
    cfg.service_generator(Box::new(Services::default()));
    cfg.compile_fds(fds.clone()).expect("prost");

    let set = prost::Message::encode_to_vec(&fds);
    fs::write(out.join("descriptor.bin"), &set).unwrap();
    pbjson_build::Builder::new()
        .register_descriptors(&set)
        .unwrap()
        .extern_path(".google.protobuf", "::pbjson_types")
        .build(&[".goap"])
        .expect("pbjson");
}

fn collect(dir: &PathBuf, files: &mut Vec<PathBuf>) {
    for e in fs::read_dir(dir).unwrap() {
        let p = e.unwrap().path();
        if p.is_dir() {
            collect(&p, files);
        } else if p.extension().is_some_and(|x| x == "proto") {
            files.push(p);
        }
    }
}

/// Emits, in each package's module, one `<Service>` descriptor: its full name and its methods (name, streaming).
#[derive(Default)]
struct Services;

impl prost_build::ServiceGenerator for Services {
    fn generate(&mut self, s: prost_build::Service, buf: &mut String) {
        let full = format!("{}.{}", s.package, s.proto_name);
        let _ = writeln!(buf, "/// The methods of `{full}`.");
        let _ = writeln!(buf, "pub mod {}_methods {{", to_snake(&s.name));
        let _ = writeln!(buf, "    pub const SERVICE: &str = \"{full}\";");
        let _ = writeln!(buf, "    /// (name, client streaming, server streaming)");
        let _ = writeln!(buf, "    pub const METHODS: &[(&str, bool, bool)] = &[");
        for m in &s.methods {
            let _ = writeln!(buf, "        (\"{}\", {}, {}),", m.proto_name, m.client_streaming, m.server_streaming);
        }
        let _ = writeln!(buf, "    ];\n}}");
    }
}

fn to_snake(s: &str) -> String {
    let mut o = String::new();
    for (i, c) in s.chars().enumerate() {
        if c.is_uppercase() && i > 0 {
            o.push('_');
        }
        o.extend(c.to_lowercase());
    }
    o
}
