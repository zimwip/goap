//! What the adapter instances of a unit's chain leave of an MCP.

use goap_mcp as mcp;

use crate::Instance;

/// Restriction is what the adapter instances of a unit's chain leave of an MCP (ADR 0028). Every instance of the
/// chain adds its restrictions: a unit narrows what its ancestors allow and hands the result down to its sub-units,
/// which cannot widen it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Restriction {
    /// Disabled: an instance of the chain removes the MCP.
    pub disabled: bool,
    /// Allow, when not None, is the intersection of the allow-lists of the chain.
    pub allow: Option<Vec<String>>,
    /// Deny is the union of the deny-lists of the chain.
    pub deny: Vec<String>,
    /// ReadOnly: an instance of the chain keeps only the read-only tools.
    pub read_only: bool,
    /// By lists the units whose instance restricts, nearest first.
    pub by: Vec<String>,
}

impl Restriction {
    /// Add adds the restrictions of an instance of the chain.
    pub fn add(&mut self, a: &Instance) {
        if !a.restricts() {
            return;
        }
        self.by.push(a.unit.clone());
        self.disabled = self.disabled || a.disabled;
        self.read_only = self.read_only || a.read_only;
        for t in &a.deny {
            if !self.deny.contains(t) {
                self.deny.push(t.clone());
            }
        }
        if !a.tools.is_empty() {
            match &mut self.allow {
                None => self.allow = Some(a.tools.clone()),
                Some(allow) => allow.retain(|t| a.tools.contains(t)),
            }
        }
    }

    /// Allows reports whether a tool of the MCP is allowed.
    pub fn allows(&self, t: &mcp::Tool) -> bool {
        if self.disabled || (self.read_only && !t.read_only) || self.deny.contains(&t.name) {
            return false;
        }
        match &self.allow {
            Some(allow) => allow.contains(&t.name),
            None => true,
        }
    }

    /// Apply returns the definition with only the allowed tools.
    pub fn apply(&self, d: &mcp::Def) -> mcp::Def {
        let mut out = d.clone();
        out.tools = d.tools.iter().filter(|t| self.allows(t)).cloned().collect();
        out
    }
}
