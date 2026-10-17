//! The dependency rules between the concepts of the platform (CLAUDE.md, design rule 1;
//! ADR 0061): an MCP knows no connector and no adapter, the algorithm library knows no MCP, a domain knows no adapter,
//! the graph and the domain model know no use case (the risk register) nor the policy of the engine and of the agents
//! (the relaunch of a step, the guidance, the confidence an agent ruling needs: ADR 0067), and the adapter is the one
//! place where MCP and organisation meet.

use goap_layering::{DepKind, Source, Workspace, root, sources};
use regex::Regex;

/// By package, the packages it must not depend on, directly or not.
const FORBIDDEN: &[(&str, &[&str])] = &[
    (
        "contracts/mcp",
        &[
            "lib/adapter",
            "lib/mcpbuiltin",
            "lib/access",
            "contracts/algo",
            "contracts/domain",
            "graph/graph",
        ],
    ),
    (
        "contracts/algo",
        &[
            "contracts/mcp",
            "lib/adapter",
            "lib/mcpbuiltin",
            "lib/access",
        ],
    ),
    (
        "lib/dsl",
        &[
            "contracts/mcp",
            "lib/adapter",
            "lib/mcpbuiltin",
            "lib/access",
        ],
    ),
    (
        "lib/def",
        &[
            "contracts/mcp",
            "lib/adapter",
            "lib/mcpbuiltin",
            "lib/access",
        ],
    ),
    (
        "lib/access",
        &[
            "contracts/mcp",
            "lib/adapter",
            "lib/mcpbuiltin",
            "graph/graph",
            "engine/engine",
        ],
    ),
    (
        "lib/llmcfg",
        &[
            "contracts/mcp",
            "lib/adapter",
            "lib/mcpbuiltin",
            "graph/graph",
            "engine/engine",
        ],
    ),
    // The methodology describes and compiles; the engine plans and runs (ADR 0126): neither the compiler nor the
    // conditions know the planner.
    ("engine/methodology", &["engine/goap", "engine/engine"]),
    ("engine/condition", &["engine/goap", "engine/engine"]),
    (
        "engine/engine",
        &[
            "lib/adapter",
            "lib/mcpbuiltin",
            "engine/observe",
            "engine/selfimprove",
            "platform/assistantsvc",
            "graph/graph",
        ],
    ),
    (
        "contracts/builtins",
        &["engine/methodology", "engine/engine", "contracts/domain"],
    ),
    ("lib/adapter", &["lib/mcpbuiltin", "lib/access"]),
    (
        "contracts/domain",
        &[
            "contracts/risk",
            "engine/methodology",
            "contracts/journal",
            "contracts/decision",
            "contracts/verify",
            "contracts/criticality",
            "contracts/review",
        ],
    ),
    (
        "contracts/criticality",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "contracts/journal",
            "contracts/decision",
            "engine/condition",
            "lib/access",
        ],
    ),
    (
        "contracts/verify",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "contracts/risk",
            "contracts/journal",
            "contracts/decision",
            "engine/condition",
        ],
    ),
    (
        "contracts/decision",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "contracts/risk",
            "contracts/journal",
        ],
    ),
    (
        "contracts/journal",
        &["graph/graph", "engine/engine", "engine/methodology"],
    ),
    (
        "graph/graph",
        &[
            "contracts/risk",
            "engine/methodology",
            "contracts/journal",
            "contracts/decision",
            "contracts/verify",
            "contracts/criticality",
            "contracts/review",
            "platform/registrysvc",
            "platform/assistantsvc",
            "engine/engine",
        ],
    ),
    // The engine and the graph meet only through the contract of the graph (engine::GraphPort over goap-graphapi):
    // the engine names no graph implementation, the graph and its service no engine type (docs/architecture.md,
    // "Components").
    (
        "contracts/graphapi",
        &["graph/graph", "engine/engine", "engine/methodology"],
    ),
    ("graph/graphsvc", &["engine/engine", "engine/methodology"]),
    // The gateway, the MCP hub and what every service shares reach the graph and the engine through their contracts
    // and clients only, never their implementation (ADR 0125).
    (
        "platform/cmd/gateway",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "gateway/gateway",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "gateway/credsvc",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "mcp/mcpsvc",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "mcp/connectorkit",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "mcp/connectors/builtin",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "lib/graphclient",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "lib/telemetry",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "lib/pbconv",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    (
        "lib/rpcerr",
        &["graph/graph", "engine/engine", "graph/graphsvc"],
    ),
    ("lib/graphsnap", &["graph/graph", "engine/engine"]),
    (
        "platform/cmd/mcp",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "graph/graphsvc",
            "platform/registrysvc",
        ],
    ),
    // Two registries (ADR 0125 §3): the domain registry defines the graph and names no methodology; the methodology
    // registry reads it through a port and the contract, and stores its nodes through the graph's contract.
    (
        "graph/domainsvc",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "platform/registrysvc",
        ],
    ),
    (
        "lib/domainclient",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "graph/domainsvc",
            "platform/registrysvc",
        ],
    ),
    (
        "lib/domainpb",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "graph/domainsvc",
            "platform/registrysvc",
        ],
    ),
    (
        "platform/registrysvc",
        &["graph/graph", "graph/graphsvc", "graph/domainsvc"],
    ),
    (
        "contracts/review",
        &[
            "graph/graph",
            "engine/engine",
            "engine/methodology",
            "contracts/risk",
            "contracts/journal",
            "contracts/decision",
            "engine/condition",
            "lib/access",
        ],
    ),
];

/// By package, the packages its tests must not depend on either: the engine is tested without the graph (a real graph
/// runs it in platform/e2e/engine, ADR 0125 §5), so that it can stand alone.
const FORBIDDEN_IN_TESTS: &[(&str, &[&str])] = &[(
    "engine/engine",
    &[
        "graph/graph",
        "graph/graph/graphtest",
        "graph/graphsvc",
        "platform/devseed",
        "lib/mcpbuiltin",
    ],
)];

#[test]
fn layering() {
    let ws = Workspace::load().unwrap();
    let mut bad = Vec::new();
    for (pkg, banned) in FORBIDDEN {
        assert!(ws.crates.contains_key(*pkg), "no crate in {pkg}");
        let deps = ws.deps_of(pkg, &[DepKind::Normal, DepKind::Build]);
        for b in *banned {
            if deps.contains(*b) {
                bad.push(format!("{pkg} depends on {b}"));
            }
        }
    }
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}

#[test]
fn test_layering() {
    let ws = Workspace::load().unwrap();
    let mut bad = Vec::new();
    for (pkg, banned) in FORBIDDEN_IN_TESTS {
        assert!(ws.crates.contains_key(*pkg), "no crate in {pkg}");
        let deps = ws.deps_of(pkg, &[DepKind::Normal, DepKind::Build, DepKind::Dev]);
        for b in *banned {
            if deps.contains(*b) {
                bad.push(format!("the tests of {pkg} depend on {b}"));
            }
        }
    }
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}

/// The non-test sources of the crate of `pkg`, which must have some.
fn sources_of(pkg: &str) -> Vec<Source> {
    let files = sources(&root().join(pkg)).unwrap();
    assert!(!files.is_empty(), "{pkg}: no source");
    files
}

/// Fails for every non-test source of the packages holding one of `words`.
fn names(pkgs: &[&str], words: &[&str], what: impl Fn(&str) -> String) {
    let mut bad = Vec::new();
    for pkg in pkgs {
        for s in sources_of(pkg) {
            for w in words {
                if s.code.contains(w) {
                    bad.push(format!("{} {}", s.path.display(), what(w)));
                }
            }
        }
    }
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}

/// The graph core names no concept of the methodology namespace (ADR 0066: the Activity a change is scoped to lives
/// in Change.data, read by the registry through ChangeLifecycles::merge and Graph.sub_change_validator).
#[test]
fn graph_names_no_methodology() {
    names(
        &["graph/graph"],
        &[
            "methodology@",
            "NamespaceMethodology",
            "NAMESPACE_METHODOLOGY",
            "sub_activity",
            "ActivityRef",
        ],
        |w| format!("names {w:?}"),
    );
}

/// The graph core keeps the mechanism of flows and decision points; what the engine and the agents make of them is
/// theirs (ADR 0067): a flow stores an opaque origin and opaque stale run ids and opens with items it is given, a
/// decision point stores opaque policy values and asks a DecisionPolicy. The core names none of their vocabulary.
/// Go matches its fields case-sensitively; Rust fields are matched as accesses (`.from_step`), a local of that name
/// being no vocabulary.
#[test]
fn core_names_no_engine_policy() {
    names(
        &["graph/graph", "contracts/domain"],
        &[
            "\"guidance\"",
            "FromStep",
            ".from_step",
            "StaleExecutions",
            ".stale_executions",
            "Threshold",
            ".threshold",
            "MaxRounds",
            ".max_rounds",
            "MAX_ROUNDS",
            "DeciderAgent",
            "DECIDER_AGENT",
            "DeciderHuman",
            "DECIDER_HUMAN",
        ],
        |w| format!("names {w:?}"),
    );
}

/// The graph core and the domain model name no structure of the organisation (ADR 0054, ADR 0069): the node type that
/// builds each structure, its parent link, its root and its default flag come from the type catalogue (a domain tags
/// them, lib/domains/builtin/organisation.yaml for the built-in one), and an untyped graph falls back to the catalogue
/// of the built-in domains, never to names written in Rust.
#[test]
fn core_names_no_structure() {
    names(
        &["graph/graph", "contracts/domain"],
        &[
            "ORG-DEFAULT",
            "PROJ-ROOT",
            "OrgUnit",
            "ProjectUnit",
            "part_of",
            "project_part_of",
        ],
        |w| format!("names {w:?}"),
    );
}

/// The engine reads the formal elements the compiler states (ADR 0126): the call of a step (methodology::Call), the
/// owner of a step, the description of a generated condition; it decodes no name the compiler makes up (the prefixes
/// of the generated conditions, the parameters of a former builtin), and it reads the activable form only, never the
/// definition behind it (Compiled::definition, ADR 0126 step 2). `process.step` is the name of the former builtin, a
/// string literal (`ac.process.steps` is a field access).
#[test]
fn engine_decodes_no_convention() {
    let mut words = vec![
        "\"step:",
        "\"state:",
        "\"method:",
        "\"subject:",
        "\"pending:",
        "\"when:",
        "\"expect:",
        "StatePrefix",
        "STATE_PREFIX",
        "ReactionPrefix",
        "REACTION_PREFIX",
        "\"process.step",
        ".Definition()",
        ".definition()",
    ];
    let params: Vec<String> = ["step", "capability", "foreach", "groupBy", "goal"]
        .iter()
        .flat_map(|p| {
            [
                format!("Params[\"{p}\"]"),
                format!("params[\"{p}\"]"),
                format!("params.get(\"{p}\")"),
            ]
        })
        .collect();
    words.extend(params.iter().map(String::as_str));
    names(&["engine/engine"], &words, |w| {
        format!("decodes the convention {w}: read the element the compiler states instead")
    });
}

/// The engine binds the compiled view of a methodology to a change (ADR 0126 step 3): the step trees, the meaning of
/// the conditions, their producers and prerequisites come from methodology::View, never walked or computed again by
/// the engine.
#[test]
fn engine_binds_the_view() {
    names(
        &["engine/engine"],
        &[
            "process_steps(",
            "method_steps(",
            "capability_guards(",
            "condition_description(",
            ".producers(",
            ".prerequisites(",
        ],
        |w| {
            format!(
                "walks the definition with {w}: bind the compiled view (Compiled::view) instead"
            )
        },
    );
}

/// The internals of the engine (Internals, Internals::internals_of) open it to the integration tests of the
/// platform; no production code calls them.
#[test]
fn engine_internals_only_in_tests() {
    let ws = Workspace::load().unwrap();
    let uses = Regex::new(r"\binternals_of\b|\bInternals\b").unwrap();
    let mut bad = Vec::new();
    for c in ws.crates.values().filter(|c| c.pkg != "engine/engine") {
        for s in sources(&c.dir).unwrap() {
            if uses.is_match(&s.code) {
                bad.push(format!("{} uses the engine's Internals", s.path.display()));
            }
        }
    }
    assert!(bad.is_empty(), "{}", bad.join("\n"));
}
