//! Port of `docrepo.go`: the document repository of the development compositions and the workspaces of the changes.

use std::path::{Component, Path, PathBuf};

use goap_adapter as adapter;
use goap_algo as algo;
use goap_ctx::Ctx;
use goap_domain::{NAMESPACE_PLATFORM, STRUCTURE_ORGANISATION, TYPE_ADAPTER_DEF};
use goap_graph::{Error, Graph, Result};
use goap_graphsvc::{seed_adapter, seed_change, seed_node};
use goap_mcp as mcp;
use goap_mcpbuiltin as mcpbuiltin;
use serde_json::{Map, Value, json};

/// documentRepository is the generic MCP to manipulate documents.
fn document_repository_def() -> mcp::Def {
    let obj = |props: Value, required: &[&str]| -> Map<String, Value> {
        let mut s = Map::new();
        s.insert("type".into(), "object".into());
        s.insert("properties".into(), props);
        if !required.is_empty() {
            s.insert("required".into(), json!(required));
        }
        s
    };
    let str = |desc: &str| json!({"type": "string", "description": desc});
    let tool = |name: &str, description: &str, input_schema| mcp::Tool {
        name: name.into(),
        description: description.into(),
        input_schema,
        ..Default::default()
    };
    mcp::Def {
        name: "document-repository".into(),
        description: "Documents organised in folders.".into(),
        tools: vec![
            tool(
                "list",
                "List the documents and folders under a path.",
                obj(json!({"path": str("folder path, empty for the root")}), &[]),
            ),
            tool(
                "read",
                "Read the text of a document.",
                obj(json!({"path": str("document path")}), &["path"]),
            ),
            tool(
                "write",
                "Create or replace a document.",
                obj(
                    json!({"path": str("document path"), "content": str("text of the document")}),
                    &["path", "content"],
                ),
            ),
        ],
        ..Default::default()
    }
}

/// LocalFSAdapterName is the adapter definition of the document-repository MCP on the localfs connector.
pub const LOCAL_FS_ADAPTER_NAME: &str = "localfs-document-repository";

/// localFSAdapterDef is the reference adapter: the document-repository MCP on the local file system connector.
fn local_fs_adapter_def() -> adapter::Def {
    adapter::Def {
        name: LOCAL_FS_ADAPTER_NAME.into(),
        description: "The document-repository MCP on the local file system connector (localfs)"
            .into(),
        mcp: "document-repository".into(),
        connector: "localfs".into(),
        language: algo::JAVASCRIPT.into(),
        params: vec![algo::Param {
            name: "root".into(),
            r#type: algo::PARAM_STRING.into(),
            required: true,
            description: "Directory the unit exposes as its document repository".into(),
            ..Default::default()
        }],
        code: r#"switch (ctx.tool()) {
  case "list":
    return ctx.call("list_dir", { path: ctx.args().path || "" });
  case "read":
    return ctx.call("read_file", { path: ctx.args().path });
  case "write":
    return ctx.call("write_file", { path: ctx.args().path, content: ctx.args().content });
}
ctx.fail("unknown tool " + ctx.tool());
"#
        .into(),
    }
}

/// LocalFSAdapter is the instance of the localfs adapter of the platform library for a unit, exposing a directory as
/// its document repository (demos and tests).
pub fn local_fs_adapter(unit: &str, root: &str) -> adapter::Instance {
    let mut params = Map::new();
    params.insert("root".into(), root.into());
    adapter::Instance {
        unit: unit.into(),
        mcp: "document-repository".into(),
        adapter: LOCAL_FS_ADAPTER_NAME.into(),
        params,
        ..Default::default()
    }
}

/// DocumentRepository makes sure the document-repository MCP and the definition of its localfs adapter exist in the
/// platform namespace. It is idempotent: once the MCP exists (edited or deleted since) nothing is touched. It reports
/// whether it seeded. The graph is bootstrapped first (goap_graphsvc::boot).
pub async fn document_repository(ctx: &Ctx, g: &Graph) -> Result<bool> {
    let d = document_repository_def();
    match g
        .node_by_key(ctx, NAMESPACE_PLATFORM, &mcp::mcp_key(&d.name))
        .await
    {
        Ok(_) => return Ok(false),
        Err(e) if !e.is_not_found() => return Err(e),
        Err(_) => {}
    }
    let a = local_fs_adapter_def();
    seed_change(
        ctx,
        g,
        NAMESPACE_PLATFORM,
        &format!("MCP {}", d.name),
        vec![
            seed_node(&mcp::mcp_key(&d.name), mcp::NODE_TYPE_MCP, d.props()),
            seed_node(&adapter::def_key(&a.name), TYPE_ADAPTER_DEF, a.props()),
        ],
    )
    .await?;
    Ok(true)
}

/// LocalFS gives the default organisation its document repository on the directory root (the localfs adapter),
/// unless it already holds an instance of the MCP. [`document_repository`] runs first.
pub async fn local_fs(ctx: &Ctx, g: &Graph, root: &str) -> Result<()> {
    let unit = g.structure(STRUCTURE_ORGANISATION).root;
    match g
        .node_by_key(
            ctx,
            "organisation",
            &adapter::key(&unit, "document-repository"),
        )
        .await
    {
        Ok(_) => return Ok(()),
        Err(e) if !e.is_not_found() => return Err(e),
        Err(_) => {}
    }
    seed_adapter(ctx, g, &local_fs_adapter(&unit, root)).await
}

/// Workspace gives the default organisation the workspaces of its changes under the directory root (the local
/// workspace adapter, ADR 0118), unless it already holds an instance of the workspace MCP. The MCP and its adapter
/// definition are seeded by goap_graphsvc::seed_builtins.
pub async fn workspace(ctx: &Ctx, g: &Graph, root: &str, image: &str) -> Result<()> {
    let unit = g.structure(STRUCTURE_ORGANISATION).root;
    match g
        .node_by_key(
            ctx,
            "organisation",
            &adapter::key(&unit, mcpbuiltin::WORKSPACE),
        )
        .await
    {
        Ok(_) => return Ok(()),
        Err(e) if !e.is_not_found() => return Err(e),
        Err(_) => {}
    }
    let abs = abs(root).map_err(|e| Error::msg(e.to_string()))?;
    let abs = abs.to_string_lossy();
    if !image.is_empty() {
        return seed_adapter(
            ctx,
            g,
            &mcpbuiltin::workspace_docker_adapter(&unit, &abs, image),
        )
        .await;
    }
    seed_adapter(ctx, g, &mcpbuiltin::workspace_adapter(&unit, &abs)).await
}

/// Go's `filepath.Abs`: the path made absolute against the working directory, then cleaned lexically.
fn abs(p: &str) -> std::io::Result<PathBuf> {
    let p = std::path::absolute(Path::new(p))?;
    let mut out = PathBuf::new();
    for c in p.components() {
        match c {
            Component::CurDir => {}
            Component::ParentDir => {
                out.pop();
            }
            c => out.push(c),
        }
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn abs_cleans_like_go() {
        let p = abs("/a/b/../c/./d").unwrap();
        assert_eq!(p, PathBuf::from("/a/c/d"));
        assert!(abs("x").unwrap().is_absolute());
    }
}
