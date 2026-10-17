//! Port of `convert.go`: the methodology and its views to and from the protocol (registry.v1).

use std::collections::{BTreeMap, HashMap};

use goap_condition::{Expectation, LinkSpec, ProduceSpec};
use goap_engine::PlanPreview;
use goap_gen::registry::v1 as pb;
use goap_methodology::{
    Action, ActivityView, Agent, AssessmentState, AssessmentView, Condition, Event, Goal, Guardian,
    Integration, LevelCheck, Loop, Method, Methodology, Persistence, Process, ProcessGraph,
    Reference, Responsibilities, Review, Role, Step, StepView, Subscription, Trigger, Verify, View,
};
use goap_pbconv as pbconv;

use crate::store::Record;

fn hm<K: Clone + Eq + std::hash::Hash + Ord, V: Clone>(m: &BTreeMap<K, V>) -> HashMap<K, V> {
    m.iter().map(|(k, v)| (k.clone(), v.clone())).collect()
}

fn bm<K: Clone + Eq + std::hash::Hash + Ord, V: Clone>(m: &HashMap<K, V>) -> BTreeMap<K, V> {
    m.iter().map(|(k, v)| (k.clone(), v.clone())).collect()
}

/// ToPB converts a stored record.
pub fn to_pb(r: &Record) -> pb::Methodology {
    let m = &r.methodology;
    pb::Methodology {
        name: m.name.clone(),
        version: m.version.clone(),
        description: m.description.clone(),
        namespace: m.namespace.clone(),
        applies_to: m.applies_to.clone(),
        imports: m.imports.clone(),
        on: subs_to_pb(&m.on),
        lifecycle: m.lifecycle.clone(),
        criticality: m.criticality.clone(),
        goal: m.goal.clone(),
        integrates: integrations_to_pb(&m.integrates),
        guardian: guardian_to_pb(m.guardian.as_ref()),
        events: events_to_pb(&m.events),
        assessments: assessments_to_pb(&m.assessments),
        domains: hm(&m.domains),
        status: r.status.to_string(),
        created_at: pbconv::opt_time(&r.created_at),
        updated_at: pbconv::opt_time(&r.updated_at),
        published_at: pbconv::opt_time(&r.published_at),
        updated_by: r.updated_by.clone(),
        conditions: m.conditions.iter().map(condition_to_pb).collect(),
        actions: m.actions.iter().map(action_to_pb).collect(),
        goals: m
            .goals
            .iter()
            .map(|g| pb::Goal {
                name: g.name.clone(),
                description: g.description.clone(),
                examples: g.examples.clone(),
                pre: hm(&g.pre),
                value: g.value,
            })
            .collect(),
        agents: m.agents.iter().map(agent_to_pb).collect(),
        roles: m
            .roles
            .iter()
            .map(|r| pb::Role {
                name: r.name.clone(),
                description: r.description.clone(),
                operations: r.operations.clone(),
            })
            .collect(),
        methods: m.methods.iter().map(method_to_pb).collect(),
        processes: m
            .processes
            .iter()
            .map(|p| pb::Process {
                name: p.name.clone(),
                description: p.description.clone(),
                examples: p.examples.clone(),
                steps: steps_to_pb(&p.steps),
                references: refs_to_pb(&p.references),
                on: subs_to_pb(&p.on),
            })
            .collect(),
        suspended: String::new(),
    }
}

fn condition_to_pb(c: &Condition) -> pb::Condition {
    pb::Condition {
        name: c.name.clone(),
        description: c.description.clone(),
        expr: c.expr.clone(),
        scope: c.scope.clone(),
        r#where: c.r#where.clone(),
        all: c.all.clone(),
        any: c.any.clone(),
        none: c.none.clone(),
        empty: c.empty,
    }
}

fn action_to_pb(a: &Action) -> pb::Action {
    pb::Action {
        name: a.name.clone(),
        description: a.description.clone(),
        kind: a.kind.clone(),
        pre: hm(&a.pre),
        effects: hm(&a.effects),
        cost: a.cost,
        permission: a.permission.clone(),
        model: a.model.clone(),
        prompt: a.prompt.clone(),
        tool: a.tool.clone(),
        builtin: a.builtin.clone(),
        instructions: a.instructions.clone(),
        params: pbconv::to_struct(&a.params),
        language: a.language.clone(),
        code: a.code.clone(),
        utility: a.utility.clone(),
        specializes: a.specializes.clone(),
        when: a.when.clone(),
        priority: a.priority as i32,
        incremental: a.incremental,
        mcps: a.mcps.clone(),
        on: subs_to_pb(&a.on),
        emits: a.emits.clone(),
        scope: a.scope.clone(),
        r#where: a.r#where.clone(),
        parallel: a.parallel as i32,
        form: pbconv::form_to_pb(a.form.as_ref()),
        r#loop: loop_to_pb(a.r#loop.as_ref()),
        roles: a.roles.clone(),
        expects: a.expects.as_ref().map(|e| pb::Expectation {
            for_each: e.for_each.clone(),
            r#where: e.r#where.clone(),
            produce: Some(pb::ProduceSpec {
                op: e.produce.op.clone(),
                node_type: e.produce.node_type.clone(),
            }),
            link: e.link.as_ref().map(|l| pb::LinkSpec {
                r#type: l.r#type.clone(),
                direction: l.direction.clone(),
            }),
        }),
        verify: a.verify.as_ref().map(|v| pb::Verify {
            oracle: v.oracle.clone(),
            independent: v.independent,
        }),
    }
}

fn agent_to_pb(a: &Agent) -> pb::Agent {
    pb::Agent {
        name: a.name.clone(),
        description: a.description.clone(),
        examples: a.examples.clone(),
        planner: a.planner.clone(),
        actions: a.actions.clone(),
        goals: a.goals.clone(),
        mcps: a.mcps.clone(),
        model: a.model.clone(),
        persistent: persist_to_pb(a.persistent.as_ref()),
        roles: a.roles.clone(),
        triggers: a
            .triggers
            .iter()
            .map(|t| pb::Trigger {
                name: t.name.clone(),
                description: t.description.clone(),
                r#type: t.r#type.clone(),
                event: t.event.clone(),
                filter: t.filter.clone(),
                schedule: t.schedule.clone(),
                goal: t.goal.clone(),
                intent: t.intent.clone(),
                target: t.target.clone(),
                roles: t.roles.clone(),
                enabled: t.enabled,
                supersede: t.supersede,
            })
            .collect(),
    }
}

fn method_to_pb(me: &Method) -> pb::Method {
    pb::Method {
        name: me.name.clone(),
        r#for: me.r#for.clone(),
        when: me.when.clone(),
        priority: me.priority as i32,
        description: me.description.clone(),
        guidance: me.guidance.clone(),
        checklist: me.checklist.clone(),
        deliverables: me.deliverables.clone(),
        references: refs_to_pb(&me.references),
        roles: resp_to_pb(me.roles.as_ref()),
        steps: steps_to_pb(&me.steps),
        actions: me.actions.clone(),
        done: hm(&me.done),
        planner: me.planner.clone(),
        model: me.model.clone(),
        mcps: me.mcps.clone(),
        on: subs_to_pb(&me.on),
        persistent: persist_to_pb(me.persistent.as_ref()),
    }
}

fn steps_to_pb(steps: &[Step]) -> Vec<pb::Step> {
    steps
        .iter()
        .map(|s| pb::Step {
            name: s.name.clone(),
            description: s.description.clone(),
            instructions: s.instructions.clone(),
            pre: hm(&s.pre),
            done: hm(&s.done),
            references: refs_to_pb(&s.references),
            guidance: s.guidance.clone(),
            checklist: s.checklist.clone(),
            deliverables: s.deliverables.clone(),
            method: s.capability.clone(),
            foreach: s.foreach.clone(),
            group_by: s.group_by.clone(),
            roles: resp_to_pb(s.roles.as_ref()),
            steps: steps_to_pb(&s.steps),
            action: s.action.clone(),
            actions: s.actions.clone(),
            process: s.process.clone(),
            on: subs_to_pb(&s.on),
            r#where: s.r#where.clone(),
            review: review_to_pb(s.review.as_ref()),
        })
        .collect()
}

fn steps_from_pb(steps: &[pb::Step]) -> Vec<Step> {
    steps
        .iter()
        .map(|s| Step {
            name: s.name.clone(),
            description: s.description.clone(),
            instructions: s.instructions.clone(),
            pre: bm(&s.pre),
            done: bm(&s.done),
            references: refs_from_pb(&s.references),
            guidance: s.guidance.clone(),
            checklist: s.checklist.clone(),
            deliverables: s.deliverables.clone(),
            capability: s.method.clone(),
            foreach: s.foreach.clone(),
            group_by: s.group_by.clone(),
            roles: resp_from_pb(s.roles.as_ref()),
            steps: steps_from_pb(&s.steps),
            action: s.action.clone(),
            actions: s.actions.clone(),
            process: s.process.clone(),
            on: subs_from_pb(&s.on),
            r#where: s.r#where.clone(),
            review: review_from_pb(s.review.as_ref()),
        })
        .collect()
}

fn events_to_pb(evs: &[Event]) -> Vec<pb::EventDef> {
    evs.iter()
        .map(|e| pb::EventDef {
            name: e.name.clone(),
            description: e.description.clone(),
        })
        .collect()
}

fn events_from_pb(evs: &[pb::EventDef]) -> Vec<Event> {
    evs.iter()
        .map(|e| Event {
            name: e.name.clone(),
            description: e.description.clone(),
        })
        .collect()
}

fn assessments_to_pb(vs: &[AssessmentView]) -> Vec<pb::AssessmentView> {
    vs.iter()
        .map(|v| pb::AssessmentView {
            subject: v.subject.clone(),
            states: v
                .states
                .iter()
                .map(|s| pb::AssessmentState {
                    when: s.when.clone(),
                    conditions: hm(&s.conditions),
                    tone: s.tone.clone(),
                    label: s.label.clone(),
                    open: s.open,
                })
                .collect(),
        })
        .collect()
}

fn assessments_from_pb(vs: &[pb::AssessmentView]) -> Vec<AssessmentView> {
    vs.iter()
        .map(|v| AssessmentView {
            subject: v.subject.clone(),
            states: v
                .states
                .iter()
                .map(|s| AssessmentState {
                    when: s.when.clone(),
                    conditions: bm(&s.conditions),
                    tone: s.tone.clone(),
                    label: s.label.clone(),
                    open: s.open,
                })
                .collect(),
        })
        .collect()
}

fn subs_to_pb(subs: &[Subscription]) -> Vec<pb::Subscription> {
    subs.iter()
        .map(|s| pb::Subscription {
            event: s.event.clone(),
            filter: s.filter.clone(),
            from: s.from.clone(),
            supersede: s.supersede,
        })
        .collect()
}

fn subs_from_pb(subs: &[pb::Subscription]) -> Vec<Subscription> {
    subs.iter()
        .map(|s| Subscription {
            event: s.event.clone(),
            filter: s.filter.clone(),
            from: s.from.clone(),
            supersede: s.supersede,
        })
        .collect()
}

fn integrations_to_pb(ins: &[Integration]) -> Vec<pb::Integration> {
    ins.iter()
        .map(|i| pb::Integration {
            methodologies: i.methodologies.clone(),
            exclusive: i.exclusive,
            attach: i.attach.clone(),
        })
        .collect()
}

fn integrations_from_pb(ins: &[pb::Integration]) -> Vec<Integration> {
    ins.iter()
        .map(|i| Integration {
            methodologies: i.methodologies.clone(),
            exclusive: i.exclusive,
            attach: i.attach.clone(),
        })
        .collect()
}

fn guardian_to_pb(g: Option<&Guardian>) -> Option<pb::Guardian> {
    g.map(|g| pb::Guardian {
        merge: g.merge.clone(),
        transitions: hm(&g.transitions),
    })
}

fn guardian_from_pb(g: Option<&pb::Guardian>) -> Option<Guardian> {
    let g = g?;
    if g.merge.is_empty() && g.transitions.is_empty() {
        return None;
    }
    Some(Guardian {
        merge: g.merge.clone(),
        transitions: bm(&g.transitions),
    })
}

fn resp_to_pb(r: Option<&Responsibilities>) -> Option<pb::Responsibilities> {
    r.map(|r| pb::Responsibilities {
        responsible: r.responsible.clone(),
        accountable: r.accountable.clone(),
        consulted: r.consulted.clone(),
        informed: r.informed.clone(),
    })
}

fn resp_from_pb(r: Option<&pb::Responsibilities>) -> Option<Responsibilities> {
    r.map(|r| Responsibilities {
        responsible: r.responsible.clone(),
        accountable: r.accountable.clone(),
        consulted: r.consulted.clone(),
        informed: r.informed.clone(),
    })
}

fn refs_to_pb(refs: &[Reference]) -> Vec<pb::Reference> {
    refs.iter()
        .map(|r| pb::Reference {
            title: r.title.clone(),
            r#ref: r.r#ref.clone(),
            section: r.section.clone(),
        })
        .collect()
}

fn refs_from_pb(refs: &[pb::Reference]) -> Vec<Reference> {
    refs.iter()
        .map(|r| Reference {
            title: r.title.clone(),
            r#ref: r.r#ref.clone(),
            section: r.section.clone(),
        })
        .collect()
}

/// SummaryToPB converts a record to a list entry.
pub fn summary_to_pb(r: &Record) -> pb::MethodologySummary {
    let m = &r.methodology;
    let mut out = pb::MethodologySummary {
        name: m.name.clone(),
        version: m.version.clone(),
        description: m.description.clone(),
        status: r.status.to_string(),
        updated_at: pbconv::opt_time(&r.updated_at),
        published_at: pbconv::opt_time(&r.published_at),
        namespace: goap_domain::namespace_of(&m.namespace).to_string(),
        ..pb::MethodologySummary::default()
    };
    for g in &m.goals {
        out.goals.push(pb::GoalSummary {
            name: g.name.clone(),
            description: g.description.clone(),
        });
    }
    for a in &m.agents {
        out.agents.push(pb::AgentSummary {
            name: a.name.clone(),
            description: a.description.clone(),
            planner: a.planner.clone(),
        });
    }
    // a process is run by an agent of its name towards a goal of its name (ADR 0034)
    for p in &m.processes {
        out.goals.push(pb::GoalSummary {
            name: p.name.clone(),
            description: p.description.clone(),
        });
        out.agents.push(pb::AgentSummary {
            name: p.name.clone(),
            description: p.description.clone(),
            planner: "process".into(),
        });
    }
    out
}

/// FromPB converts an edited definition (status and timestamps are ignored).
pub fn from_pb(p: Option<&pb::Methodology>) -> Methodology {
    let Some(p) = p else {
        return Methodology::default();
    };
    Methodology {
        name: p.name.clone(),
        version: p.version.clone(),
        description: p.description.clone(),
        namespace: p.namespace.clone(),
        applies_to: p.applies_to.clone(),
        imports: p.imports.clone(),
        on: subs_from_pb(&p.on),
        lifecycle: p.lifecycle.clone(),
        criticality: p.criticality.clone(),
        goal: p.goal.clone(),
        integrates: integrations_from_pb(&p.integrates),
        guardian: guardian_from_pb(p.guardian.as_ref()),
        events: events_from_pb(&p.events),
        assessments: assessments_from_pb(&p.assessments),
        domains: bm(&p.domains),
        conditions: p
            .conditions
            .iter()
            .map(|c| Condition {
                name: c.name.clone(),
                description: c.description.clone(),
                expr: c.expr.clone(),
                scope: c.scope.clone(),
                r#where: c.r#where.clone(),
                all: c.all.clone(),
                any: c.any.clone(),
                none: c.none.clone(),
                empty: c.empty,
            })
            .collect(),
        actions: p.actions.iter().map(action_from_pb).collect(),
        goals: p
            .goals
            .iter()
            .map(|g| Goal {
                name: g.name.clone(),
                description: g.description.clone(),
                examples: g.examples.clone(),
                pre: bm(&g.pre),
                value: g.value,
            })
            .collect(),
        agents: p
            .agents
            .iter()
            .map(|a| {
                let mut ag = Agent::default();
                ag.name = a.name.clone();
                ag.description = a.description.clone();
                ag.examples = a.examples.clone();
                ag.planner = a.planner.clone();
                ag.model = a.model.clone();
                ag.actions = a.actions.clone();
                ag.goals = a.goals.clone();
                ag.mcps = a.mcps.clone();
                ag.persistent = persist_from_pb(a.persistent.as_ref());
                ag.roles = a.roles.clone();
                ag.triggers = a
                    .triggers
                    .iter()
                    .map(|t| Trigger {
                        name: t.name.clone(),
                        description: t.description.clone(),
                        r#type: t.r#type.clone(),
                        event: t.event.clone(),
                        filter: t.filter.clone(),
                        schedule: t.schedule.clone(),
                        goal: t.goal.clone(),
                        intent: t.intent.clone(),
                        target: t.target.clone(),
                        roles: t.roles.clone(),
                        enabled: t.enabled,
                        supersede: t.supersede,
                    })
                    .collect();
                ag
            })
            .collect(),
        roles: p
            .roles
            .iter()
            .map(|r| Role {
                name: r.name.clone(),
                description: r.description.clone(),
                operations: r.operations.clone(),
            })
            .collect(),
        methods: p
            .methods
            .iter()
            .map(|me| Method {
                name: me.name.clone(),
                r#for: me.r#for.clone(),
                when: me.when.clone(),
                priority: me.priority as i64,
                description: me.description.clone(),
                guidance: me.guidance.clone(),
                checklist: me.checklist.clone(),
                deliverables: me.deliverables.clone(),
                references: refs_from_pb(&me.references),
                roles: resp_from_pb(me.roles.as_ref()),
                steps: steps_from_pb(&me.steps),
                actions: me.actions.clone(),
                done: bm(&me.done),
                planner: me.planner.clone(),
                model: me.model.clone(),
                mcps: me.mcps.clone(),
                on: subs_from_pb(&me.on),
                persistent: persist_from_pb(me.persistent.as_ref()),
            })
            .collect(),
        processes: p
            .processes
            .iter()
            .map(|p| Process {
                name: p.name.clone(),
                description: p.description.clone(),
                examples: p.examples.clone(),
                steps: steps_from_pb(&p.steps),
                references: refs_from_pb(&p.references),
                on: subs_from_pb(&p.on),
            })
            .collect(),
        ..Methodology::default()
    }
}

fn action_from_pb(a: &pb::Action) -> Action {
    let mut x = Action::default();
    x.name = a.name.clone();
    x.description = a.description.clone();
    x.kind = a.kind.clone();
    x.pre = bm(&a.pre);
    x.effects = bm(&a.effects);
    x.cost = a.cost;
    x.permission = a.permission.clone();
    x.model = a.model.clone();
    x.prompt = a.prompt.clone();
    x.tool = a.tool.clone();
    x.builtin = a.builtin.clone();
    x.instructions = a.instructions.clone();
    x.params = pbconv::to_map(a.params.as_ref());
    x.language = a.language.clone();
    x.code = a.code.clone();
    x.utility = a.utility.clone();
    x.specializes = a.specializes.clone();
    x.when = a.when.clone();
    x.priority = a.priority as i64;
    x.incremental = a.incremental;
    x.mcps = a.mcps.clone();
    x.on = subs_from_pb(&a.on);
    x.emits = a.emits.clone();
    x.scope = a.scope.clone();
    x.r#where = a.r#where.clone();
    x.parallel = a.parallel as i64;
    x.form = pbconv::form_from_pb(a.form.as_ref());
    x.r#loop = loop_from_pb(a.r#loop.as_ref());
    x.roles = a.roles.clone();
    x.expects = a
        .expects
        .as_ref()
        .filter(|e| !e.for_each.is_empty())
        .map(|e| Expectation {
            for_each: e.for_each.clone(),
            r#where: e.r#where.clone(),
            produce: e
                .produce
                .as_ref()
                .map(|p| ProduceSpec {
                    op: p.op.clone(),
                    node_type: p.node_type.clone(),
                })
                .unwrap_or_default(),
            link: e
                .link
                .as_ref()
                .filter(|l| !l.r#type.is_empty())
                .map(|l| LinkSpec {
                    r#type: l.r#type.clone(),
                    direction: l.direction.clone(),
                }),
        });
    x.verify = a.verify.as_ref().map(|v| Verify {
        oracle: v.oracle.clone(),
        independent: v.independent,
    });
    x
}

/// LevelsToPB converts the level-by-level coherence of a process or method.
pub fn levels_to_pb(ls: &[LevelCheck]) -> Vec<pb::LevelCheck> {
    ls.iter()
        .map(|l| pb::LevelCheck {
            path: l.path.clone(),
            kind: l.kind.clone(),
            inputs: hm(&l.inputs),
            outputs: hm(&l.outputs),
            ok: l.ok(),
            agent: l.agent.clone(),
            goal: l.goal.clone(),
            order: l
                .order
                .iter()
                .map(|s| pb::LevelStep {
                    name: s.name.clone(),
                    layer: s.layer as i32,
                })
                .collect(),
            steps: l
                .steps
                .iter()
                .map(|n| pb::LevelNode {
                    name: n.name.clone(),
                    path: n.path.clone(),
                    method: n.method.clone(),
                    target: n.target.clone(),
                    capability: n.capability.clone(),
                    entry: hm(&n.entry),
                    exit: hm(&n.exit),
                    composite: n.composite,
                    broken: n.broken,
                    sub_steps: n.sub_steps as i32,
                    foreach: n.foreach.clone(),
                    group_by: n.group_by.clone(),
                    review: review_to_pb(n.review.as_ref()),
                })
                .collect(),
            edges: l
                .edges
                .iter()
                .map(|e| pb::GraphEdge {
                    from: e.from.clone(),
                    to: e.to.clone(),
                    conditions: e.conditions.clone(),
                })
                .collect(),
            gaps: l
                .gaps
                .iter()
                .map(|g| pb::LevelGap {
                    step: g.step.clone(),
                    kind: g.kind.clone(),
                    missing: g.missing.clone(),
                    message: g.message.clone(),
                })
                .collect(),
        })
        .collect()
}

/// ProcessGraphToPB converts the graph of a process (ADR 0036 §4).
pub fn process_graph_to_pb(g: &ProcessGraph) -> pb::ProcessGraph {
    let mut out = pb::ProcessGraph {
        process: g.process.clone(),
        description: g.description.clone(),
        references: refs_to_pb(&g.references),
        ..pb::ProcessGraph::default()
    };
    for s in &g.steps {
        out.steps.push(pb::GraphStep {
            path: s.path.clone(),
            name: s.name.clone(),
            description: s.description.clone(),
            parent: s.parent.clone(),
            depth: s.depth as i32,
            leaf: s.leaf,
            method: s.method.clone(),
            target: s.target.clone(),
            entry: hm(&s.entry),
            exit: hm(&s.exit),
            roles: resp_to_pb(s.roles.as_ref()),
            guidance: s.guidance.clone(),
            references: refs_to_pb(&s.references),
            process: s.process.clone(),
            capability: s.capability.clone(),
            foreach: s.foreach.clone(),
            group_by: s.group_by.clone(),
            review: review_to_pb(s.review.as_ref()),
        });
    }
    for e in &g.edges {
        out.edges.push(pb::GraphEdge {
            from: e.from.clone(),
            to: e.to.clone(),
            conditions: e.conditions.clone(),
        });
    }
    for me in &g.methods {
        out.methods.push(method_to_pb(&me.method));
        out.method_goals
            .insert(me.method.name.clone(), me.agent_goal.clone());
    }
    for a in &g.agents {
        out.agents.push(pb::GraphAgent {
            name: a.name.clone(),
            description: a.description.clone(),
            planner: a.planner.clone(),
            goals: a.goals.clone(),
            actions: a
                .actions
                .iter()
                .map(|x| pb::GraphAction {
                    name: x.name.clone(),
                    kind: x.kind.clone(),
                    description: x.description.clone(),
                    pre: hm(&x.pre),
                    effects: hm(&x.effects),
                })
                .collect(),
        });
    }
    out
}

/// PlanPreviewToPB converts the result of planning a goal from a (possibly overridden) world state.
pub fn plan_preview_to_pb(p: &PlanPreview) -> pb::PlanPreview {
    pb::PlanPreview {
        agent: p.agent.clone(),
        goal: p.goal.clone(),
        planner: p.planner.clone(),
        reached: p.reached,
        cost: p.cost,
        awaiting: p.awaiting.clone(),
        actions: p
            .actions
            .iter()
            .map(|a| pb::PlanStep {
                name: a.name.clone(),
                step: a.step.clone(),
                kind: a.kind.clone(),
                cost: a.cost,
            })
            .collect(),
    }
}

fn review_to_pb(r: Option<&Review>) -> Option<pb::StepReview> {
    r.map(|r| pb::StepReview {
        subject: r.subject.clone(),
        automatic: r.automatic,
    })
}

fn review_from_pb(r: Option<&pb::StepReview>) -> Option<Review> {
    r.map(|r| Review {
        subject: r.subject.clone(),
        automatic: r.automatic,
    })
}

/// persistToPB and persistFromPB convert the persistence of an agent or a method (ADR 0112).
fn persist_to_pb(p: Option<&Persistence>) -> Option<pb::Persistence> {
    p.map(|p| pb::Persistence {
        scope: p.scope.clone(),
        until: hm(&p.until),
        memory: p.memory as i32,
    })
}

fn persist_from_pb(p: Option<&pb::Persistence>) -> Option<Persistence> {
    p.map(|p| Persistence {
        scope: p.scope.clone(),
        until: bm(&p.until),
        memory: p.memory as i64,
    })
}

fn loop_to_pb(l: Option<&Loop>) -> Option<pb::ActionLoop> {
    l.map(|l| pb::ActionLoop {
        max_rounds: l.max_rounds as i32,
        max_tool_result_bytes: l.max_tool_result_bytes as i32,
        max_tokens: l.max_tokens,
        timeout: l.timeout.clone(),
        compact_bytes: l.compact_bytes as i32,
    })
}

fn loop_from_pb(l: Option<&pb::ActionLoop>) -> Option<Loop> {
    l.map(|l| Loop {
        max_rounds: l.max_rounds as i64,
        max_tool_result_bytes: l.max_tool_result_bytes as i64,
        max_tokens: l.max_tokens,
        timeout: l.timeout.clone(),
        compact_bytes: l.compact_bytes as i64,
    })
}

/// MethodologyViewToPB converts the compiled view of a methodology (ADR 0126 step 3).
pub fn methodology_view_to_pb(v: &View) -> pb::MethodologyView {
    pb::MethodologyView {
        methodology: v.methodology.clone(),
        version: v.version.clone(),
        goal: v.goal.clone(),
        processes: activity_views_to_pb(&v.processes),
        methods: activity_views_to_pb(&v.methods),
        conditions: v
            .conditions
            .iter()
            .map(|(name, c)| {
                (
                    name.clone(),
                    pb::ViewCondition {
                        description: c.description.clone(),
                        produced_by: c.produced_by.clone(),
                        requires: c.requires.clone(),
                    },
                )
            })
            .collect(),
    }
}

fn activity_views_to_pb(r#as: &[ActivityView]) -> Vec<pb::ViewActivity> {
    r#as.iter()
        .map(|a| pb::ViewActivity {
            name: a.name.clone(),
            kind: a.kind.clone(),
            description: a.description.clone(),
            agent: a.agent.clone(),
            goal: a.goal.clone(),
            done: hm(&a.done),
            nested: a.nested,
            reactive: a.reactive,
            r#for: a.r#for.clone(),
            roles: resp_to_pb(a.roles.as_ref()),
            steps: step_views_to_pb(&a.steps),
        })
        .collect()
}

fn step_views_to_pb(steps: &[StepView]) -> Vec<pb::ViewStep> {
    steps
        .iter()
        .map(|s| pb::ViewStep {
            path: s.path.clone(),
            name: s.name.clone(),
            owner: s.owner.clone(),
            method: s.method.clone(),
            target: s.target.clone(),
            capability: s.capability.clone(),
            foreach: s.foreach.clone(),
            description: s.description.clone(),
            guidance: s.guidance.clone(),
            checklist: s.checklist.clone(),
            references: refs_to_pb(&s.references),
            roles: resp_to_pb(s.roles.as_ref()),
            entry: hm(&s.entry),
            exit: hm(&s.exit),
            planned: s.planned.clone(),
            unavailable: s.unavailable.clone(),
            steps: step_views_to_pb(&s.steps),
            review: review_to_pb(s.review.as_ref()),
        })
        .collect()
}
