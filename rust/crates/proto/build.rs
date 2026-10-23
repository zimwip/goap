//! Compiles proto/ into the message types (prost), their protobuf JSON forms (pbjson, the forms Connect's JSON
//! codec and the web speak) and, per service, the table of its methods, its server trait, its router and its client
//! over goap-connect (see `Services`).
use std::{env, fmt::Write as _, fs, path::PathBuf};

fn main() {
    let root = PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap()).join("../../../proto");
    let mut files: Vec<PathBuf> = Vec::new();
    collect(&root, &mut files);
    files.sort();
    println!("cargo:rerun-if-changed={}", root.display());
    let fds = protox::compile(&files, [&root]).expect("compile proto/");
    let out = PathBuf::from(env::var("OUT_DIR").unwrap());

    let mut cfg = prost_build::Config::new();
    cfg.compile_well_known_types();
    cfg.extern_path(".google.protobuf", "::pbjson_types");
    cfg.service_generator(Box::new(Services));
    cfg.compile_fds(fds.clone()).expect("prost");

    let set = prost::Message::encode_to_vec(&fds);
    fs::write(out.join("descriptor.bin"), &set).unwrap();
    pbjson_build::Builder::new()
        .register_descriptors(&set)
        .unwrap()
        .extern_path(".google.protobuf", "::pbjson_types")
        // connect-go reads JSON with DiscardUnknown: a field this version does not know is ignored, not refused
        .ignore_unknown_fields()
        .build(&[".goap"])
        .expect("pbjson");

    // the module tree: one module per package, its messages (prost) and their JSON forms (pbjson)
    let mut packages: Vec<String> = fds
        .file
        .iter()
        .filter_map(|f| f.package.clone())
        .filter(|p| p.starts_with("goap."))
        .collect();
    packages.sort();
    packages.dedup();
    let mut tree = String::new();
    for p in &packages {
        let parts: Vec<&str> = p.trim_start_matches("goap.").split('.').collect();
        for part in &parts {
            let _ = write!(tree, "pub mod {part} {{ ");
        }
        let _ = write!(
            tree,
            "include!(concat!(env!(\"OUT_DIR\"), \"/{p}.rs\")); include!(concat!(env!(\"OUT_DIR\"), \"/{p}.serde.rs\")); "
        );
        tree.push_str(&"}".repeat(parts.len()));
        tree.push('\n');
    }
    fs::write(out.join("packages.rs"), tree).unwrap();
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

/// Emits, in each package's module, per service `Foo`:
///
/// - `foo_methods`: its full name and its methods (name, client streaming, server streaming);
/// - `trait Foo`: one async method per RPC taking the context and a `goap_connect::Request`, answering a
///   `goap_connect::Response` (of a `goap_connect::ServerStream` for a server stream); the default bodies answer
///   `unimplemented`, as connect-go's `UnimplementedFooHandler` does;
/// - `foo_router(Arc<dyn Foo>, Interceptors) -> axum::Router`: the routes of the service;
/// - `FooClient`: the client, implementing `Foo` over a `goap_connect::Client`.
///
/// Client and bidirectional streaming methods (none in proto/) are left out of the trait, the router and the client.
#[derive(Default)]
struct Services;

const C: &str = "::goap_connect";

impl prost_build::ServiceGenerator for Services {
    fn generate(&mut self, s: prost_build::Service, buf: &mut String) {
        let full = format!("{}.{}", s.package, s.proto_name);
        let snake = to_snake(&s.name);
        let name = &s.name;
        let _ = writeln!(buf, "/// The methods of `{full}`.");
        let _ = writeln!(buf, "pub mod {snake}_methods {{");
        let _ = writeln!(buf, "    pub const SERVICE: &str = \"{full}\";");
        let _ = writeln!(buf, "    /// (name, client streaming, server streaming)");
        let _ = writeln!(buf, "    pub const METHODS: &[(&str, bool, bool)] = &[");
        for m in &s.methods {
            let _ = writeln!(
                buf,
                "        (\"{}\", {}, {}),",
                m.proto_name, m.client_streaming, m.server_streaming
            );
        }
        let _ = writeln!(buf, "    ];\n}}");

        let methods: Vec<_> = s.methods.iter().filter(|m| !m.client_streaming).collect();

        // the server trait
        let _ = writeln!(
            buf,
            "/// The service `{full}`: implement the methods it serves (the others answer `unimplemented`),"
        );
        let _ = writeln!(
            buf,
            "/// then mount [`{snake}_router`]. [`{name}Client`] implements it over the network."
        );
        doc(buf, &s.comments.leading, "");
        let _ = writeln!(buf, "#[{C}::async_trait]");
        let _ = writeln!(
            buf,
            "pub trait {name}: ::core::marker::Send + ::core::marker::Sync + 'static {{"
        );
        for m in &methods {
            doc(buf, &m.comments.leading, "    ");
            let _ = writeln!(buf, "    {} {{", signature(m));
            let _ = writeln!(buf, "        let _ = (ctx, req);");
            let _ = writeln!(
                buf,
                "        ::core::result::Result::Err({C}::ConnectError::unimplemented_procedure(\"/{full}/{}\"))",
                m.proto_name
            );
            let _ = writeln!(buf, "    }}");
        }
        let _ = writeln!(buf, "}}");

        // the router
        let _ = writeln!(
            buf,
            "/// The routes of `{full}` served by `svc`, every call going through `interceptors`."
        );
        let _ = writeln!(
            buf,
            "pub fn {snake}_router(svc: ::std::sync::Arc<dyn {name}>, interceptors: {C}::Interceptors) -> {C}::axum::Router {{"
        );
        let _ = writeln!(buf, "    {C}::ServiceRouter::new(\"{full}\", interceptors)");
        for m in &methods {
            let kind = if m.server_streaming { "server_stream" } else { "unary" };
            let _ = writeln!(
                buf,
                "        .{kind}(\"{p}\", {{ let s = svc.clone(); move |ctx: {C}::Ctx, req: {C}::Request<{i}>| {{ let s = s.clone(); async move {{ s.{n}(&ctx, req).await }} }} }})",
                p = m.proto_name,
                i = m.input_type,
                n = m.name
            );
        }
        let _ = writeln!(buf, "        .build()\n}}");

        // the client
        let _ = writeln!(
            buf,
            "/// The client of `{full}`: the methods of [`{name}`] over a `goap_connect::Client`."
        );
        let _ = writeln!(buf, "#[derive(Clone, Debug)]");
        let _ = writeln!(buf, "pub struct {name}Client({C}::Client);");
        let _ = writeln!(buf, "impl {name}Client {{");
        let _ = writeln!(
            buf,
            "    /// The client of the service at the base URL of `client`, with its settings."
        );
        let _ = writeln!(buf, "    pub fn new(client: {C}::Client) -> Self {{");
        let _ = writeln!(buf, "        Self(client)\n    }}");
        let _ = writeln!(
            buf,
            "    /// The client of the service at `base_url`, default settings (binary codec)."
        );
        let _ = writeln!(buf, "    pub fn connect(base_url: &str) -> Self {{");
        let _ = writeln!(buf, "        Self({C}::Client::new(base_url))\n    }}");
        let _ = writeln!(buf, "    pub fn inner(&self) -> &{C}::Client {{");
        let _ = writeln!(buf, "        &self.0\n    }}");
        let _ = writeln!(buf, "}}");
        let _ = writeln!(buf, "#[{C}::async_trait]");
        let _ = writeln!(buf, "impl {name} for {name}Client {{");
        for m in &methods {
            let call = if m.server_streaming { "server_stream" } else { "unary" };
            let _ = writeln!(buf, "    {} {{", signature(m));
            let _ = writeln!(
                buf,
                "        self.0.{call}(ctx, \"/{full}/{}\", req).await",
                m.proto_name
            );
            let _ = writeln!(buf, "    }}");
        }
        let _ = writeln!(buf, "}}");
    }
}

/// The signature of the method of an RPC.
fn signature(m: &prost_build::Method) -> String {
    let out = if m.server_streaming {
        format!("{C}::Response<{C}::ServerStream<{}>>", m.output_type)
    } else {
        format!("{C}::Response<{}>", m.output_type)
    };
    format!(
        "async fn {}(&self, ctx: &{C}::Ctx, req: {C}::Request<{}>) -> ::core::result::Result<{out}, {C}::ConnectError>",
        m.name, m.input_type
    )
}

/// Writes proto comments as doc lines, flattened so that rustdoc finds no code block in them.
fn doc(buf: &mut String, lines: &[String], indent: &str) {
    for l in lines {
        let l = l.trim().replace("```", "'''");
        let _ = writeln!(buf, "{indent}#[doc = {:?}]", format!(" {l}"));
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
