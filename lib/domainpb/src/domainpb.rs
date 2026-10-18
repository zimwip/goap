//! Package domainpb converts the domains between their definition (`goap_def`) and the messages of the registry
//! protocol (registry.v1): the contract both registries speak (ADR 0125 §3).

use goap_algo as algo;
use goap_def as def;
use goap_domain as domain;
use goap_gen::registry::v1 as pb;
use goap_pbconv as pbconv;
use goap_typecat as typecat;

/// IssuesToPB converts validation issues.
pub fn issues_to_pb(is: &def::Issues) -> Vec<pb::Issue> {
    is.0.iter()
        .map(|x| pb::Issue {
            path: x.path.clone(),
            message: x.message.clone(),
            activity: x.activity.clone(),
            severity: x.severity.clone(),
            hint: x.hint.clone(),
        })
        .collect()
}

/// IssuesFromPB is the inverse of IssuesToPB.
pub fn issues_from_pb(is: &[pb::Issue]) -> def::Issues {
    def::Issues(
        is.iter()
            .map(|x| def::Issue {
                path: x.path.clone(),
                message: x.message.clone(),
                activity: x.activity.clone(),
                severity: x.severity.clone(),
                hint: x.hint.clone(),
            })
            .collect(),
    )
}

fn node_types_to_pb(ns: &[def::NodeType]) -> Vec<pb::NodeType> {
    ns.iter().map(node_type_to_pb).collect()
}

fn link_types_to_pb(ls: &[def::LinkType]) -> Vec<pb::LinkType> {
    ls.iter()
        .map(|l| pb::LinkType {
            name: l.name.clone(),
            description: l.description.clone(),
            from: l.from.clone(),
            to: l.to.clone(),
            compose: l.compose,
            attributes: attributes_to_pb(&l.attributes),
            validators: l.validators.clone(),
            label: l.label.clone(),
            inverse_label: l.inverse_label.clone(),
        })
        .collect()
}

/// DomainToPB converts a stored domain record.
pub fn domain_to_pb(r: &def::Record) -> pb::Domain {
    let d = &r.domain;
    pb::Domain {
        name: d.name.clone(),
        version: d.version.clone(),
        description: d.description.clone(),
        status: r.status.to_string(),
        node_types: node_types_to_pb(&d.node_types),
        link_types: link_types_to_pb(&d.link_types),
        enums: enums_to_pb(&d.enums),
        lifecycles: lifecycles_to_pb(&d.lifecycles),
        algorithms: algorithms_to_pb(&d.algorithms),
        algorithm_instances: instances_to_pb(&d.instances),
        item_types: item_types_to_pb(&d.item_types),
        created_at: pbconv::opt_time(&r.created_at),
        updated_at: pbconv::opt_time(&r.updated_at),
        published_at: pbconv::opt_time(&r.published_at),
        updated_by: r.updated_by.clone(),
        builtin: r.builtin,
    }
}

/// DomainSummaryToPB converts a record to a list entry.
pub fn domain_summary_to_pb(r: &def::Record) -> pb::DomainSummary {
    let d = &r.domain;
    pb::DomainSummary {
        name: d.name.clone(),
        version: d.version.clone(),
        description: d.description.clone(),
        status: r.status.to_string(),
        node_type_count: d.node_types.len() as i32,
        link_type_count: d.link_types.len() as i32,
        item_type_count: d.item_types.len() as i32,
        updated_at: pbconv::opt_time(&r.updated_at),
        published_at: pbconv::opt_time(&r.published_at),
        builtin: r.builtin,
    }
}

/// DomainFromPB converts an edited domain (status and timestamps are ignored; the empty domain for None).
pub fn domain_from_pb(p: Option<&pb::Domain>) -> def::Domain {
    let Some(p) = p else {
        return def::Domain::default();
    };
    let mut d = def::Domain {
        name: p.name.clone(),
        version: p.version.clone(),
        description: p.description.clone(),
        ..Default::default()
    };
    d.schema.node_types = p.node_types.iter().map(node_type_from_pb).collect();
    d.schema.link_types = p
        .link_types
        .iter()
        .map(|l| def::LinkType {
            name: l.name.clone(),
            description: l.description.clone(),
            from: l.from.clone(),
            to: l.to.clone(),
            compose: l.compose,
            attributes: attributes_from_pb(&l.attributes),
            validators: l.validators.clone(),
            label: l.label.clone(),
            inverse_label: l.inverse_label.clone(),
        })
        .collect();
    d.schema.enums = enums_from_pb(&p.enums);
    d.schema.lifecycles = lifecycles_from_pb(&p.lifecycles);
    d.schema.algorithms = algorithms_from_pb(&p.algorithms);
    d.schema.instances = instances_from_pb(&p.algorithm_instances);
    d.schema.item_types = item_types_from_pb(&p.item_types);
    d
}

fn item_types_to_pb(ts: &[def::ItemType]) -> Vec<pb::ItemType> {
    ts.iter()
        .map(|t| pb::ItemType {
            name: t.name.clone(),
            description: t.description.clone(),
            attributes: attributes_to_pb(&t.attributes),
        })
        .collect()
}

fn item_types_from_pb(ts: &[pb::ItemType]) -> Vec<def::ItemType> {
    ts.iter()
        .map(|t| def::ItemType {
            name: t.name.clone(),
            description: t.description.clone(),
            attributes: attributes_from_pb(&t.attributes),
        })
        .collect()
}

fn node_type_to_pb(n: &def::NodeType) -> pb::NodeType {
    pb::NodeType {
        name: n.name.clone(),
        description: n.description.clone(),
        attributes: attributes_to_pb(&n.attributes),
        extends: n.extends.clone(),
        lifecycle: n.lifecycle.clone(),
        change_controlled: n.change_controlled,
        validators: n.validators.clone(),
        search: search_to_pb(&n.search),
        editor: n.editor.clone(),
        admin_only: n.admin_only,
        additional_properties: n.additional_properties,
        form: pbconv::form_to_pb(n.form.as_ref()),
        document: n.document.as_ref().map(|d| pb::DocumentSpec {
            contains: d.contains.clone(),
        }),
        structure: n.structure.as_ref().map(|t| pb::StructureTag {
            kind: t.kind.clone(),
            parent: t.parent.clone(),
            root: t.root.clone(),
            self_parent: t.self_parent,
        }),
        requires: n
            .requires
            .iter()
            .map(|r| pb::RequiredLink {
                link: r.link.clone(),
                count: r.count as i32,
            })
            .collect(),
    }
}

fn node_type_from_pb(n: &pb::NodeType) -> def::NodeType {
    def::NodeType {
        name: n.name.clone(),
        description: n.description.clone(),
        attributes: attributes_from_pb(&n.attributes),
        extends: n.extends.clone(),
        lifecycle: n.lifecycle.clone(),
        change_controlled: n.change_controlled,
        validators: n.validators.clone(),
        search: search_from_pb(&n.search),
        editor: n.editor.clone(),
        admin_only: n.admin_only,
        additional_properties: n.additional_properties,
        form: pbconv::form_from_pb(n.form.as_ref()),
        document: n.document.as_ref().map(|d| domain::DocumentSpec {
            contains: d.contains.clone(),
        }),
        structure: n.structure.as_ref().map(|t| def::StructureTag {
            kind: t.kind.clone(),
            parent: t.parent.clone(),
            root: t.root.clone(),
            self_parent: t.self_parent,
            ..Default::default()
        }),
        requires: n
            .requires
            .iter()
            .map(|r| domain::RequiredLink {
                link: r.link.clone(),
                count: i64::from(r.count),
            })
            .collect(),
    }
}

pub fn lifecycles_to_pb(ls: &[domain::Lifecycle]) -> Vec<pb::Lifecycle> {
    ls.iter()
        .map(|l| pb::Lifecycle {
            name: l.name.clone(),
            description: l.description.clone(),
            initial: l.initial.clone(),
            states: l
                .states
                .iter()
                .map(|s| pb::LifecycleState {
                    name: s.name.clone(),
                    description: s.description.clone(),
                    not_landable: s.not_landable,
                    r#final: s.r#final,
                })
                .collect(),
            transitions: l
                .transitions
                .iter()
                .map(|t| pb::LifecycleTransition {
                    name: t.name.clone(),
                    description: t.description.clone(),
                    from: t.from.clone(),
                    to: t.to.clone(),
                    permission: t.permission.clone(),
                    guard: t.guard.clone(),
                    requires_attributes: t.requires.attributes.clone(),
                    requires_outgoing_links: t.requires.outgoing_links.clone(),
                    guards: t.guards.clone(),
                    actions: t.actions.clone(),
                    vetos: criteria_to_pb(&t.vetos),
                    objectives: criteria_to_pb(&t.objectives),
                    children_states: t
                        .children
                        .as_ref()
                        .map(|c| c.states.clone())
                        .unwrap_or_default(),
                })
                .collect(),
        })
        .collect()
}

fn criteria_to_pb(cs: &[domain::Criterion]) -> Vec<pb::Criterion> {
    cs.iter()
        .map(|c| pb::Criterion {
            name: c.name.clone(),
            expr: c.expr.clone(),
            description: c.description.clone(),
        })
        .collect()
}

fn criteria_from_pb(cs: &[pb::Criterion]) -> Vec<domain::Criterion> {
    cs.iter()
        .map(|c| domain::Criterion {
            name: c.name.clone(),
            expr: c.expr.clone(),
            description: c.description.clone(),
        })
        .collect()
}

pub(crate) fn lifecycles_from_pb(ls: &[pb::Lifecycle]) -> Vec<domain::Lifecycle> {
    ls.iter()
        .map(|l| domain::Lifecycle {
            name: l.name.clone(),
            description: l.description.clone(),
            initial: l.initial.clone(),
            states: l
                .states
                .iter()
                .map(|s| domain::LifecycleState {
                    name: s.name.clone(),
                    description: s.description.clone(),
                    not_landable: s.not_landable,
                    r#final: s.r#final,
                })
                .collect(),
            transitions: l
                .transitions
                .iter()
                .map(|t| domain::Transition {
                    name: t.name.clone(),
                    description: t.description.clone(),
                    from: t.from.clone(),
                    to: t.to.clone(),
                    permission: t.permission.clone(),
                    guard: t.guard.clone(),
                    requires: domain::TransitionRequires {
                        attributes: t.requires_attributes.clone(),
                        outgoing_links: t.requires_outgoing_links.clone(),
                    },
                    guards: t.guards.clone(),
                    actions: t.actions.clone(),
                    vetos: criteria_from_pb(&t.vetos),
                    objectives: criteria_from_pb(&t.objectives),
                    children: (!t.children_states.is_empty()).then(|| domain::ChildrenRule {
                        states: t.children_states.clone(),
                    }),
                    ..Default::default()
                })
                .collect(),
        })
        .collect()
}

fn search_to_pb(input: &[def::SearchProperty]) -> Vec<pb::SearchProperty> {
    input
        .iter()
        .map(|s| pb::SearchProperty {
            property: s.property.clone(),
            text: s.text,
            facet: s.facet,
        })
        .collect()
}

fn search_from_pb(input: &[pb::SearchProperty]) -> Vec<def::SearchProperty> {
    input
        .iter()
        .map(|s| def::SearchProperty {
            property: s.property.clone(),
            text: s.text,
            facet: s.facet,
        })
        .collect()
}

fn algorithms_to_pb(algs: &[algo::Algorithm]) -> Vec<pb::Algorithm> {
    algs.iter()
        .map(|a| pb::Algorithm {
            name: a.name.clone(),
            description: a.description.clone(),
            r#type: a.r#type.to_string(),
            language: a.language.clone(),
            code: a.code.clone(),
            params: a
                .params
                .iter()
                .map(|p| pb::AlgorithmParam {
                    name: p.name.clone(),
                    r#type: p.r#type.clone(),
                    description: p.description.clone(),
                    required: p.required,
                    values: p.values.clone(),
                    default_value: p.default.as_ref().map(pbconv::json_to_value),
                })
                .collect(),
        })
        .collect()
}

pub fn algorithms_from_pb(algs: &[pb::Algorithm]) -> Vec<algo::Algorithm> {
    algs.iter()
        .map(|a| algo::Algorithm {
            name: a.name.clone(),
            description: a.description.clone(),
            r#type: algo::Usage::from(a.r#type.clone()),
            language: a.language.clone(),
            code: a.code.clone(),
            params: a
                .params
                .iter()
                .map(|p| algo::Param {
                    name: p.name.clone(),
                    r#type: p.r#type.clone(),
                    description: p.description.clone(),
                    required: p.required,
                    values: p.values.clone(),
                    default: p.default_value.as_ref().map(pbconv::value_to_json),
                })
                .collect(),
        })
        .collect()
}

fn instances_to_pb(is: &[algo::Instance]) -> Vec<pb::AlgorithmInstance> {
    is.iter()
        .map(|i| pb::AlgorithmInstance {
            name: i.name.clone(),
            description: i.description.clone(),
            algorithm: i.algorithm.clone(),
            values: pbconv::to_struct(&i.values),
        })
        .collect()
}

fn instances_from_pb(is: &[pb::AlgorithmInstance]) -> Vec<algo::Instance> {
    is.iter()
        .map(|i| algo::Instance {
            name: i.name.clone(),
            description: i.description.clone(),
            algorithm: i.algorithm.clone(),
            values: pbconv::to_map(i.values.as_ref()),
        })
        .collect()
}

fn attribute_to_pb(a: &def::Attribute) -> pb::Attribute {
    pb::Attribute {
        name: a.name.clone(),
        label: a.label.clone(),
        description: a.description.clone(),
        r#type: a.r#type.clone(),
        widget: a.widget.clone(),
        r#enum: a.r#enum.clone(),
        default_value: a.default.clone(),
        section: a.section.clone(),
        order: a.order as i32,
        tooltip: a.tooltip.clone(),
        as_name: a.as_name,
        validators: a.validators.clone(),
    }
}

fn attributes_to_pb(attrs: &[def::Attribute]) -> Vec<pb::Attribute> {
    attrs.iter().map(attribute_to_pb).collect()
}

fn attributes_from_pb(attrs: &[pb::Attribute]) -> Vec<def::Attribute> {
    attrs
        .iter()
        .map(|a| def::Attribute {
            name: a.name.clone(),
            label: a.label.clone(),
            description: a.description.clone(),
            r#type: a.r#type.clone(),
            widget: a.widget.clone(),
            r#enum: a.r#enum.clone(),
            default: a.default_value.clone(),
            section: a.section.clone(),
            order: i64::from(a.order),
            tooltip: a.tooltip.clone(),
            as_name: a.as_name,
            validators: a.validators.clone(),
        })
        .collect()
}

pub fn attribute_infos_to_pb(attrs: &[typecat::Attribute]) -> Vec<pb::AttributeInfo> {
    attrs
        .iter()
        .map(|a| pb::AttributeInfo {
            attribute: Some(attribute_to_pb(&a.attribute)),
            from: a.from.clone(),
            values: enum_values_to_pb(&a.values),
        })
        .collect()
}

fn enum_values_to_pb(vs: &[def::EnumValue]) -> Vec<pb::EnumValue> {
    vs.iter()
        .map(|v| pb::EnumValue {
            value: v.value.clone(),
            label: v.label.clone(),
        })
        .collect()
}

fn enums_to_pb(es: &[def::Enum]) -> Vec<pb::Enum> {
    es.iter()
        .map(|e| pb::Enum {
            name: e.name.clone(),
            description: e.description.clone(),
            values: enum_values_to_pb(&e.values),
        })
        .collect()
}

fn enums_from_pb(es: &[pb::Enum]) -> Vec<def::Enum> {
    es.iter()
        .map(|e| def::Enum {
            name: e.name.clone(),
            description: e.description.clone(),
            values: e
                .values
                .iter()
                .map(|v| def::EnumValue {
                    value: v.value.clone(),
                    label: v.label.clone(),
                })
                .collect(),
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    // The vetos and objectives of a gate survive the PB round trip (ADR 0075 §3).
    #[test]
    fn gate_criteria_round_trip_through_pb() {
        let ls = vec![domain::Lifecycle {
            name: "l".into(),
            initial: "a".into(),
            states: vec![
                domain::LifecycleState {
                    name: "a".into(),
                    ..Default::default()
                },
                domain::LifecycleState {
                    name: "b".into(),
                    ..Default::default()
                },
            ],
            transitions: vec![domain::Transition {
                name: "go".into(),
                from: "a".into(),
                to: "b".into(),
                vetos: vec![domain::Criterion {
                    name: "safe".into(),
                    expr: r#"world["safe"]"#.into(),
                    description: "no blocker".into(),
                }],
                objectives: vec![domain::Criterion {
                    name: "docs".into(),
                    expr: r#"world["docs"]"#.into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        }];
        let back = lifecycles_from_pb(&lifecycles_to_pb(&ls));
        assert_eq!(back, ls, "lifecycle changed through PB");
    }

    #[test]
    fn issues_round_trip_keep_severity() {
        let input = def::Issues(vec![
            def::Issue {
                path: "goal".into(),
                message: "w".into(),
                severity: def::SEVERITY_WARNING.into(),
                ..Default::default()
            },
            def::Issue {
                path: "name".into(),
                message: "e".into(),
                activity: "p/s".into(),
                ..Default::default()
            },
        ]);
        let out = issues_from_pb(&issues_to_pb(&input));
        assert!(
            out.0.len() == 2
                && out.0[0] == input.0[0]
                && out.0[1] == input.0[1]
                && out.0[0].is_warning()
                && !out.0[1].is_warning(),
            "{out:?}"
        );
    }

    // The enums of a domain survive the PB round trip.
    #[test]
    fn enums_round_trip_through_pb() {
        let d = def::Domain {
            name: "alm".into(),
            version: "1".into(),
            schema: def::Schema {
                enums: vec![def::Enum {
                    name: "level".into(),
                    values: vec![def::EnumValue {
                        value: "low".into(),
                        label: "Low".into(),
                    }],
                    ..Default::default()
                }],
                ..Default::default()
            },
            ..Default::default()
        };
        let rec = def::Record {
            domain: d.clone(),
            ..Default::default()
        };
        let back = domain_from_pb(Some(&domain_to_pb(&rec)));
        assert_eq!(back.schema.enums, d.schema.enums, "enums");
    }

    // A domain crosses the RPC with its enums and its item types (ADR 0128).
    #[test]
    fn domain_item_types_round_trip() {
        let d = def::Domain {
            name: "itemtest".into(),
            version: "1.0.0".into(),
            schema: def::Schema {
                enums: vec![def::Enum {
                    name: "verdict".into(),
                    values: vec![
                        def::EnumValue {
                            value: "pass".into(),
                            ..Default::default()
                        },
                        def::EnumValue {
                            value: "fail".into(),
                            ..Default::default()
                        },
                    ],
                    ..Default::default()
                }],
                item_types: vec![def::ItemType {
                    name: "testResult".into(),
                    description: "The result of a test run".into(),
                    attributes: vec![def::Attribute {
                        name: "verdict".into(),
                        r#type: def::ATTR_ENUM.into(),
                        r#enum: "verdict".into(),
                        ..Default::default()
                    }],
                }],
                ..Default::default()
            },
            ..Default::default()
        };
        let rec = def::Record {
            domain: d.clone(),
            ..Default::default()
        };
        let pb = domain_to_pb(&rec);
        let got = domain_from_pb(Some(&pb));
        assert!(
            got.schema.item_types == d.schema.item_types
                && got.schema.enums.len() == 1
                && got.schema.enums[0].values.len() == 2,
            "round trip: {got:?}"
        );
        let s = domain_summary_to_pb(&rec);
        assert_eq!(s.item_type_count, 1, "summary: {s:?}");
    }
}
