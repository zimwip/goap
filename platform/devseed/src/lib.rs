//! Port of `platform/devseed` (see docs/rust-port.md): the data of the development and demo compositions, the ALM demo
//! repository and the local file system document repository. It is separate from the platform bootstrap
//! (`goap_graphsvc::boot`), which every composition runs. The demo is loaded on demand (the "Load demo data" setting,
//! RPC LoadDemoData) or at start when asked (GOAP_GRAPH_SEED=demo for the graph service, GOAP_DEV_DEMO=1 for
//! goap-dev); the document repository is seeded by goap-dev at every start.
//!
//! API (every function bootstraps nothing but [`demo`], which runs `Graph::bootstrap` first):
//!
//! - `pub async fn demo(ctx: &Ctx, g: &Graph) -> goap_graph::Result<bool>`: the demo organisation (ORG-ACME and its
//!   units), the demo project [`DEMO_PROJECT`] with its assignment, and the alm data; true when it wrote anything.
//! - `pub fn demo_loader(d: Option<Arc<goap_access::Directory>>) -> goap_graphsvc::DemoFn`: [`demo`] as
//!   `Handler::demo` takes it (loaded, demo project key), refreshing the directory after a load.
//! - `pub async fn document_repository(ctx: &Ctx, g: &Graph) -> goap_graph::Result<bool>`: the document-repository MCP
//!   and its localfs adapter definition ([`LOCAL_FS_ADAPTER_NAME`]), once.
//! - `pub async fn local_fs(ctx: &Ctx, g: &Graph, root: &str) -> goap_graph::Result<()>`: the default organisation's
//!   document repository on a directory; [`local_fs_adapter`] is the instance.
//! - `pub async fn workspace(ctx: &Ctx, g: &Graph, root: &str, image: &str) -> goap_graph::Result<()>`: the default
//!   organisation's workspaces (local, or docker when `image` is set).
//!
//! As a `goap_graphsvc::DevFn` (Options::dev), wrap them: `dev_fn(|ctx, g| Box::pin(async move { ... }))`.

mod demo;
mod docrepo;

pub use demo::{DEMO_PROJECT, demo, demo_loader};
pub use docrepo::{
    LOCAL_FS_ADAPTER_NAME, document_repository, local_fs, local_fs_adapter, workspace,
};
