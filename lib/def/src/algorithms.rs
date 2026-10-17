//! The algorithms of a domain (ADR 0018): their check, where their instances are plugged, and their binding.

use std::collections::{HashMap, HashSet};

use goap_algo::{
    Algorithm, Bound, GO, Instance, JAVASCRIPT, USAGE_LINK_VALIDATOR, USAGE_NODE_VALIDATOR,
    USAGE_PROPERTY_VALIDATOR, USAGE_TRANSITION_ACTION, USAGE_TRANSITION_GUARD, Usage,
};
use goap_domain::Lifecycle;

use crate::model::{Issues, LinkType, NodeType};
use crate::schema::Schema;

impl Schema {
    /// propertiesOf lists the attributes of a node type, its own and the inherited ones.
    fn properties_of(&self, name: &str) -> HashSet<String> {
        let by_name = self.by_name();
        let mut out = HashSet::new();
        let mut seen = HashSet::new();
        let mut name = name;
        while !name.is_empty() && seen.insert(name) {
            let t = by_name.get(name);
            if let Some(t) = t {
                out.extend(t.attributes.iter().map(|a| a.name.clone()));
            }
            name = t.map(|t| t.extends.as_str()).unwrap_or("");
        }
        out
    }

    /// checkAlgorithms validates the algorithms, their instances and where the instances are plugged (ADR 0018).
    pub(crate) fn check_algorithms(&self, prefix: &str, issues: &mut Issues) {
        let mut algs: HashMap<&str, &Algorithm> = HashMap::new();
        for (i, a) in self.algorithms.iter().enumerate() {
            let path = format!("{prefix}algorithms[{i}]");
            for msg in a.issues() {
                issues.add(path.clone(), msg);
            }
            if !a.name.is_empty() {
                if algs.contains_key(a.name.as_str()) {
                    issues.add(
                        format!("{path}.name"),
                        format!("duplicate algorithm {}", a.name),
                    );
                }
                algs.insert(&a.name, a);
            }
            if !a.code.is_empty()
                && (a.language == JAVASCRIPT || a.language == GO)
                && let Err(e) = goap_dsl::check_algorithm_code(&a.language, &a.code)
            {
                issues.add(format!("{path}.code"), e.to_string());
            }
        }
        let mut insts: HashMap<&str, &Instance> = HashMap::new();
        for (i, inst) in self.instances.iter().enumerate() {
            let path = format!("{prefix}algorithmInstances[{i}]");
            if inst.name.is_empty() {
                issues.add(format!("{path}.name"), "name required");
            } else if !goap_algo::valid_name(&inst.name) {
                issues.add(
                    format!("{path}.name"),
                    "name must be lowercase letters, digits, '-' or '_' and start with a letter",
                );
            } else {
                if insts.contains_key(inst.name.as_str()) {
                    issues.add(
                        format!("{path}.name"),
                        format!("duplicate algorithm instance {}", inst.name),
                    );
                }
                insts.insert(&inst.name, inst);
            }
            let Some(a) = algs.get(inst.algorithm.as_str()) else {
                issues.add(
                    format!("{path}.algorithm"),
                    format!("unknown algorithm {:?}", inst.algorithm),
                );
                continue;
            };
            let (_, problems) = a.resolve(&inst.values);
            for msg in problems {
                issues.add(format!("{path}.values"), msg);
            }
        }
        let plug = |issues: &mut Issues, path: String, instance: &str, want: &Usage| {
            let Some(inst) = insts.get(instance) else {
                issues.add(path, format!("unknown algorithm instance {instance}"));
                return;
            };
            if let Some(a) = algs.get(inst.algorithm.as_str())
                && a.r#type != *want
            {
                issues.add(
                    path,
                    format!(
                        "instance {instance} is of type {}, expected {want}",
                        a.r#type
                    ),
                );
            }
        };
        for (i, n) in self.node_types.iter().enumerate() {
            for (j, a) in n.attributes.iter().enumerate() {
                for (k, v) in a.validators.iter().enumerate() {
                    plug(
                        issues,
                        format!("{prefix}nodeTypes[{i}].attributes[{j}].validators[{k}]"),
                        v,
                        &USAGE_PROPERTY_VALIDATOR,
                    );
                }
            }
            for (j, v) in n.validators.iter().enumerate() {
                plug(
                    issues,
                    format!("{prefix}nodeTypes[{i}].validators[{j}]"),
                    v,
                    &USAGE_NODE_VALIDATOR,
                );
            }
        }
        for (i, l) in self.link_types.iter().enumerate() {
            for (j, v) in l.validators.iter().enumerate() {
                plug(
                    issues,
                    format!("{prefix}linkTypes[{i}].validators[{j}]"),
                    v,
                    &USAGE_LINK_VALIDATOR,
                );
            }
            for (j, a) in l.attributes.iter().enumerate() {
                for (k, v) in a.validators.iter().enumerate() {
                    plug(
                        issues,
                        format!("{prefix}linkTypes[{i}].attributes[{j}].validators[{k}]"),
                        v,
                        &USAGE_PROPERTY_VALIDATOR,
                    );
                }
            }
        }
        let mut props: HashMap<&str, HashSet<String>> = HashMap::new();
        for (i, n) in self.node_types.iter().enumerate() {
            for (j, sp) in n.search.iter().enumerate() {
                let known = props
                    .entry(&n.name)
                    .or_insert_with(|| self.properties_of(&n.name));
                if !known.contains(&sp.property) {
                    issues.add(
                        format!("{prefix}nodeTypes[{i}].search[{j}].property"),
                        format!("unknown property {:?} of {}", sp.property, n.name),
                    );
                } else if !sp.text && !sp.facet {
                    issues.add(
                        format!("{prefix}nodeTypes[{i}].search[{j}]"),
                        format!("property {:?} is neither text nor facet", sp.property),
                    );
                }
            }
        }
        for (i, l) in self.lifecycles.iter().enumerate() {
            for (j, t) in l.transitions.iter().enumerate() {
                for (k, g) in t.guards.iter().enumerate() {
                    plug(
                        issues,
                        format!("{prefix}lifecycles[{i}].transitions[{j}].guards[{k}]"),
                        g,
                        &USAGE_TRANSITION_GUARD,
                    );
                }
                for (k, a) in t.actions.iter().enumerate() {
                    plug(
                        issues,
                        format!("{prefix}lifecycles[{i}].transitions[{j}].actions[{k}]"),
                        a,
                        &USAGE_TRANSITION_ACTION,
                    );
                }
            }
        }
    }

    /// BoundValidators returns the validators of a node type resolved with their algorithm, in call order:
    /// the supertypes' first, and for each type the validators of its attributes (in declaration order, bound
    /// to the attribute) then its node validators. Instances that do not resolve are left out (Validate reports
    /// them).
    pub fn bound_validators(&self, name: &str) -> Vec<Bound> {
        let by_name = self.by_name();
        let mut chain: Vec<&NodeType> = Vec::new();
        let mut seen = HashSet::new();
        let mut name = name;
        while !name.is_empty() && seen.insert(name) {
            let t = by_name.get(name);
            if let Some(t) = t {
                chain.insert(0, t);
            }
            name = t.map(|t| t.extends.as_str()).unwrap_or("");
        }
        chain
            .into_iter()
            .flat_map(|n| self.own_bound_validators(n))
            .collect()
    }

    /// OwnBoundValidators is BoundValidators of the type itself, without the inherited ones.
    pub fn own_bound_validators(&self, t: &NodeType) -> Vec<Bound> {
        let set = self.algorithm_set();
        let mut out = Vec::new();
        for a in &t.attributes {
            for v in &a.validators {
                if let Ok(mut b) = set.bind(v, &USAGE_PROPERTY_VALIDATOR) {
                    b.property = a.name.clone();
                    out.push(b);
                }
            }
        }
        for v in &t.validators {
            if let Ok(b) = set.bind(v, &USAGE_NODE_VALIDATOR) {
                out.push(b);
            }
        }
        out
    }

    /// OwnBoundLinkValidators resolves the validators of a link type: the property validators of its attributes (bound
    /// to the attribute, the value checked being the link property of that name) and its link validators. Instances
    /// that do not resolve are left out (Validate reports them).
    pub fn own_bound_link_validators(&self, l: &LinkType) -> (Vec<Bound>, Vec<Bound>) {
        let set = self.algorithm_set();
        let mut attributes = Vec::new();
        for a in &l.attributes {
            for v in &a.validators {
                if let Ok(mut b) = set.bind(v, &USAGE_PROPERTY_VALIDATOR) {
                    b.property = a.name.clone();
                    attributes.push(b);
                }
            }
        }
        let links = l
            .validators
            .iter()
            .filter_map(|v| set.bind(v, &USAGE_LINK_VALIDATOR).ok())
            .collect();
        (attributes, links)
    }

    /// BindLifecycle returns a copy of the lifecycle with the guard and action instances of its transitions resolved
    /// (`guard_algos`, `action_algos`).
    pub fn bind_lifecycle(&self, l: Option<&Lifecycle>) -> Option<Lifecycle> {
        let l = l?;
        let set = self.algorithm_set();
        let mut c = l.clone();
        for t in &mut c.transitions {
            t.guard_algos = t
                .guards
                .iter()
                .filter_map(|g| set.bind(g, &USAGE_TRANSITION_GUARD).ok())
                .collect();
            t.action_algos = t
                .actions
                .iter()
                .filter_map(|a| set.bind(a, &USAGE_TRANSITION_ACTION).ok())
                .collect();
        }
        Some(c)
    }
}

#[cfg(test)]
mod tests {
    use goap_algo::Algorithm;

    use super::*;

    /// A domain knows no adapter: an adapter is an AdapterDef node of the platform namespace (goap-adapter).
    #[test]
    fn domain_rejects_adapter_algorithms() {
        let d = Schema {
            algorithms: vec![Algorithm {
                name: "a".into(),
                r#type: Usage::new("adapter"),
                language: JAVASCRIPT.into(),
                code: "return 1".into(),
                ..Algorithm::default()
            }],
            ..Schema::default()
        };
        let mut issues = Issues::default();
        d.check_algorithms("", &mut issues);
        assert!(
            !issues.is_empty() && issues[0].to_string().contains("unknown type"),
            "issues = {issues}"
        );
    }
}
