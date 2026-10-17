//! Port of `structure.go`. It places every change and every node version in the two structures of the graph (ADR
//! 0054): the organisation (who is responsible) and the project (where the work happens). They are the node types the
//! type catalogue tags (`structure:` on a node type of a domain), with their parent link and root; an untyped graph
//! uses the catalogue of the built-in domains.

use std::collections::HashSet;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{
    Change, MAIN_BRANCH, Node, RequiredLink, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT, Structure,
    Structures, namespace_of,
};

use crate::graph::TypeCatalog;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// builtinTypes is the catalogue of the built-in domains alone: what an untyped graph (tests, tools) and a catalogue
/// that does not resolve a question fall back to, so that the built-in definitions have one source (their yaml).
pub(crate) fn builtin_types() -> Arc<dyn TypeCatalog> {
    goap_typecat::builtin()
}

impl Graph {
    /// Structure returns the hierarchy of a kind (STRUCTURE_ORGANISATION, STRUCTURE_PROJECT) in force.
    pub fn structure(&self, kind: &str) -> Structure {
        if let Some(c) = self.catalog()
            && let Some(s) = c.structure(kind)
        {
            return s.clone();
        }
        builtin_types().structure(kind).cloned().unwrap_or_default()
    }

    /// Structures returns the structures in force with the types belonging to each (ADR 0054): what the services
    /// reading the organisation learn from the graph service (GetStructures) instead of naming the types themselves.
    pub fn structures(&self) -> Structures {
        match self.catalog() {
            Some(c) => c.structures(),
            None => builtin_types().structures(),
        }
    }

    /// structures returns the structures in force, in bootstrap order: the organisation first, the project next, any
    /// other declared structure after.
    pub(crate) fn bootstrap_structures(&self) -> Vec<Structure> {
        let all = self.structures();
        let mut out = vec![
            self.structure(STRUCTURE_ORGANISATION),
            self.structure(STRUCTURE_PROJECT),
        ];
        for x in all.iter() {
            if x.kind != STRUCTURE_ORGANISATION && x.kind != STRUCTURE_PROJECT {
                out.push(x.structure.clone());
            }
        }
        out
    }

    /// requires returns the links a node of the type must carry (ADR 0065), in force: the type catalogue's, the
    /// built-in ones for an untyped graph.
    pub(crate) fn requires(&self, typ: &str) -> Vec<RequiredLink> {
        if let Some(c) = self.catalog() {
            let rs = c.requires(typ);
            if !rs.is_empty() {
                return rs.to_vec();
            }
        }
        builtin_types().requires(typ).to_vec()
    }

    /// AdminOnlyType reports a node type whose nodes only platform administrators write (ADR 0068): the type
    /// catalogue's `adminOnly:` flag, plus the built-in ones for an untyped graph. The one question every write path
    /// asks.
    pub fn admin_only_type(&self, typ: &str) -> bool {
        if let Some(c) = self.catalog()
            && c.admin_only(typ)
        {
            return true;
        }
        builtin_types().admin_only(typ)
    }

    /// isA reports whether the node type typ is base or a subtype of it.
    pub(crate) fn is_a(&self, typ: &str, base: &str) -> bool {
        if typ == base {
            return true;
        }
        match self.catalog() {
            Some(c) => c.is_a(typ, base),
            None => builtin_types().is_a(typ, base),
        }
    }

    /// inStructure reports whether a node belongs to a structure: of its namespace and of its type or a subtype.
    pub(crate) fn in_structure(&self, n: &Node, st: &Structure) -> bool {
        namespace_of(&n.namespace) == st.namespace && self.is_a(&n.r#type, &st.r#type)
    }

    /// structureNode resolves the key of a node of a structure: it must exist, live on main and be of the structure.
    pub(crate) async fn structure_node(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        kind: &str,
        key: &str,
    ) -> Result<Node> {
        let st = self.structure(kind);
        let what = match kind {
            STRUCTURE_ORGANISATION => "organisational unit",
            STRUCTURE_PROJECT => "project",
            _ => "",
        };
        let id = match tx.node_id_by_key(ctx, &st.namespace, key).await {
            Err(e) if e.is_not_found() => {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("{what} {key:?} is not a node of namespace {}", st.namespace),
                ));
            }
            r => r?,
        };
        let n = match tx.latest_on(ctx, &id, MAIN_BRANCH).await {
            Err(e) if e.is_not_found() => {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("{what} {key:?} is not on main yet"),
                ));
            }
            r => r?,
        };
        if n.deleted {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("{what} {key:?} is deleted"),
            ));
        }
        if !self.in_structure(&n, &st) {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("{what} {key:?} is a {}, not a {}", n.r#type, st.r#type),
            ));
        }
        Ok(n)
    }

    /// within reports whether the node of a structure with key `key` is `ancestor` or below it, following the parent
    /// links (child -> parent) of the latest versions on main.
    pub(crate) async fn within(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        kind: &str,
        key: &str,
        ancestor: &str,
    ) -> Result<bool> {
        let st = self.structure(kind);
        let mut seen = HashSet::new();
        let mut cur = key.to_string();
        while !cur.is_empty() && !seen.contains(&cur) {
            if cur == ancestor {
                return Ok(true);
            }
            seen.insert(cur.clone());
            let id = tx.node_id_by_key(ctx, &st.namespace, &cur).await?;
            let n = tx.latest_on(ctx, &id, MAIN_BRANCH).await?;
            let links = tx.out_links(ctx, &n.r#ref()).await?;
            cur = String::new();
            if let Some(l) = links.iter().find(|l| l.r#type == st.parent) {
                let p = tx.node(ctx, &l.to).await?;
                cur = p.key;
            }
        }
        Ok(false)
    }

    /// scopeChange resolves and checks who holds a change and where it acts (ADR 0054, 0091): an unset owner is the
    /// root unit; a project is never defaulted (a sub-change inherited its parent's already, a root change names its
    /// own, ADR 0091). Both must designate live nodes of their structures; nothing is left empty.
    pub(crate) async fn scope_change(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &mut Change,
    ) -> Result<()> {
        if c.owner_org.is_empty() {
            c.owner_org = self.structure(STRUCTURE_ORGANISATION).root;
        }
        if c.project_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                "a change names the project it acts in",
            ));
        }
        self.structure_node(ctx, tx, STRUCTURE_ORGANISATION, &c.owner_org)
            .await
            .map_err(|e| e.context("owner of the change"))?;
        self.structure_node(ctx, tx, STRUCTURE_PROJECT, &c.project_id)
            .await
            .map_err(|e| e.context("project of the change"))?;
        Ok(())
    }
}
