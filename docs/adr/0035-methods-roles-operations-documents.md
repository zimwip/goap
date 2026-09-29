# ADR 0035 — Methods, roles, operations and documents

**Status**: accepted (decisions below; implemented in phases, see the Phasing table) · **Date**: 2026-09 ·
Builds on ADR 0009 §5 (specializations), ADR 0012–0015 (types, domains, namespaces), ADR 0014 (lifecycles and
documents), ADR 0016 (sub-changes), ADR 0018 (library and instance), ADR 0020 (access control), ADR 0034 (processes and
steps). Relates to ADR 0033 (request to shipped change).

## Context

ADR 0034 gave methodologies processes: steps and sub-steps, each done by an action, alternatives, an agent, a nested
process or a person. Four things are still missing to make them the backbone of the work:

1. **Methods.** Agents and actions are *tools*. A **method** is how a step is carried out *in a context* (what is being
   worked on, which technology): which tools, which guidance, which deliverables. The same step ("build", "test",
   "deploy") has several methods, and the planner must pick the one that fits the context, then plan with that
   method's tools. A method is, in effect, a specialized agent.
2. **Guidance, roles and responsibilities.** A process must guide whoever does a step (person or agent): what the step
   is for, what it needs, what it produces, when it is done. It also states **who** is responsible, accountable,
   consulted and informed. Those roles are what access management (who may do what) should be fed with.
3. **Operations ("run").** In the SDLC, delivery ends when the change is deployed, but operating the system is
   continuous: monitoring, incidents, backups, capacity. A change can modify how the system is operated (a new
   runbook, a new alert), and that modification must take effect **from the deployment of the change**, environment by
   environment, while the run itself never stops.
4. **Documents.** Documents aggregate what a methodology produces (release notes, designs, test reports, runbooks),
   are versioned, and exist for every kind of work. They need an ontology of their own, belonging to a **transverse
   methodology** that any process can use at any time.

Two constraints of the current model come up repeatedly below and are named here:

- **C1 — a change acts on one namespace** (ADR 0015). A delivery change in `alm` cannot also update a document or a
  methodology.
- **C2 — the engine runs the latest published version of a methodology.** There is no notion of "the version in force
  for this scope from this date".

## Proposal

### 1. Methods: context-specific ways to do a step

A methodology declares **methods**. A method is the **documentary reference** of how a step capability is carried out
in a context (what is being worked on, which technology): its guidance, its reference documents, its deliverables, the
roles it involves. **A method is not an actor: it names its agent**, which is the actor (planner, actions, MCPs,
model), even when the method amounts to one action (the agent then has that one action). Action specializations
(`specializes` / `when` / `priority`) stay for one-action variations inside an agent.

```yaml
methods:
  - name: build_java
    for: build                       # the capability (a step names it; several methods provide it)
    when: 'changeImpacts.exists(n, "alm@Component" in n.types && n.post.props.technology == "java")'
    priority: 10
    description: Maven build, JDK 21, unit tests and SBOM
    guidance: |                      # shown to the person, given to the agent in its prompts
      Build with `mvn -B verify`; publish the SBOM; the artifact version follows the component version.
    references: [{title: Java build standard, ref: "document-repository:build/java.md"}]
    deliverables: [BuildReport]
    roles: {responsible: developer, accountable: tech_lead}
    agent: java_builder              # the actor: an agent of the methodology
    goal: build_components           # default: the agent's only goal
  - name: build_generic
    for: build
    agent: builder
```

- A step names a capability: `{name: build, method: build}`. At execution, `process.step` evaluates the `when` of
  every method providing `build` on the blackboard, keeps the applicable ones whose agent the unit holding the change
  can run (its MCPs resolve, design rule 5), and runs the agent of the one with the highest priority as a sub-agent,
  with the method's guidance and references in its context. A step's exit criteria default to what every method's
  goal makes true (the criteria they share).
- The planner therefore selects tools in two stages, both from the definitions: the **method** from the context,
  then the **actions** within the method's agent by planning.
- `agent:` on a step stays the case of a step with a single way of working and no reference of its own.

### 2. Guidance, roles and responsibilities

**Guidance.** Steps and methods gain `guidance` (markdown), `inputs` (what must be available: conditions or node types),
`deliverables` (document types, §4) and `checklist` (items a person ticks, a human task form). The engine exposes the
current step to:

- human tasks: the task shows the process, the step, its guidance, its checklist and its deliverables;
- LLM prompts: `{{ .Step }}` (process, path, guidance, deliverables, role) is available in every prompt, and the
  default system prompt of an agent running a step includes it.

**Roles.** A methodology declares the roles it needs, with what they do; steps and methods assign them, RACI style:

```yaml
roles:
  - {name: developer, description: Builds and tests the components}
  - {name: tech_lead, description: Accountable for the technical quality of a delivery}
  - {name: release_manager, description: Plans and approves deployments}
steps:
  - name: rollout
    method: deploy
    roles: {responsible: release_manager, accountable: service_owner, informed: [support]}
```

Design rule 1 holds: the methodology names **roles**, never users or units. The organisation **assigns** roles to users
per unit (`organisation@User` has roles today; the proposal is a role held *in a unit*: `holds_role` from a user to a
unit with the role name, inherited down `part_of`). Access control uses them through **one generic policy family**,
not one rule per methodology:

| Rule | Resource | Action |
|---|---|---|
| `hasRoleIn(r.sub, r.obj.Role, r.obj.Org)` | `step` | `perform` (submit a human task, approve its outputs) |
| `hasRoleIn(r.sub, r.obj.Accountable, r.obj.Org) && r.sub.Subject != r.obj.Owner` | `step` | `approve` |

The resource of a step carries its role names (from the definition) and the unit holding the change. Consequences:

- a human task is **addressed** to the role in the unit (ADR 0033 §5: the work list of a user is the tasks of the roles
  they hold);
- an agent running a step acts with the step's responsible role (its service identity carries it), so what an agent
  may do is bounded like a person in that role;
- the Access screen lists the roles every published methodology declares, so assigning them is the whole of the
  access work for a new methodology; unknown or unassigned roles are reported (a step nobody can perform).

The existing permissions (`change:apply`, `release:deploy`) become the `accountable` role of the corresponding steps;
they stay valid as they are.

### 3. Progress of a process

A read model computed from the definition and the journal, served by the engine
(`GetProcessProgress(processId)`), per step of the tree:

| State | Meaning |
|---|---|
| `done` | its exit criteria hold (and when it ran: who, when, the run) |
| `skipped` | its criteria already held when its turn came (nothing to do) |
| `active` | the step being carried out: its run, or its sub-process (nested inline) |
| `waiting` | waiting for someone: the role and unit it is addressed to, what is expected |
| `blocked` | cannot start: which entry conditions are missing, or stuck / failed with the reason |
| `todo` | not reached yet |

The change page shows it as the first thing on the Summary (ADR 0033 §6: stage, waiting on, next step), the assistant
shows it on the change card, and nested processes unfold in place. It is recomputed on every process event, not stored.

### 4. Operations: the run is continuous, a change modifies it from its deployment

**Recommended: run procedures are data, delivered with the release; the run methodology is generic.**

- An **operating methodology** (`operations`) holds **operating processes**: monitoring, incident, backup and restore,
  capacity. They are not tied to a change: they are started by triggers (schedule, alert, event) **per operated
  scope** (an application in an environment), and they never "complete"; each occurrence (an incident, a nightly
  backup) is one run.
- What is specific to one application — its runbook steps, thresholds, contacts, which method variant to use — is
  **domain data** (design rule 7, library and instance): `alm@OperatingProcedure` nodes (or documents of that type,
  §5) linked to the application and contained in its **release**. A change that modifies how the application is
  operated modifies those nodes, like any other part of the delivery.
- The procedure **in force** in an environment is the version contained in the release **deployed** there
  (`Deployment → of_release → Release → contains → OperatingProcedure@version`). A graph query
  `EffectiveIn(scope, node)` resolves it, and the operating processes read their procedure through it. So the new
  procedure applies in `dev` when the release reaches `dev`, in production when it reaches production, and a rollback
  of the release rolls the procedure back. No methodology version changes; the run never stops.

This needs two changes in how delivery lands (today every deployment lands on `main` only when the whole change is
applied, after production):

- **The change is applied when its release is approved, and shipped when its rollout ends.** `apply` lands the
  specification, design, artifacts, release and procedures (a release baseline); each deployment wave is then recorded
  as it happens by a small **standard change** (pre-approved by the methodology, auto-applied, owned by the operating
  unit) so the run sees each environment switch at once. The delivery change gets a `shipped` stage when the last wave
  is recorded (ADR 0033 §6).
- **Runs write through standard changes too.** An incident record, a backup report: each occurrence that writes to the
  graph opens a standard change of the operating methodology, applied at the end of the run (design rule 2 holds, the
  run stays auditable; ITIL's "standard change").

**When the generic run methodology itself changes** (a new kind of check for every application), it is a methodology
version, but it must not switch everywhere at publication (C2). Proposal: operating processes **pin** the methodology
version per operated scope (`runs: operations@2.3.0` on the scope's deployment), and a release that requires a newer
one declares it; the deployment moves the pin. The delivery engine keeps "latest published" for change processes.

Alternative considered: one run methodology per application, modified by the change itself. It couples every delivery
to a methodology publication, needs cross-namespace changes (C1) and version pinning anyway, and mixes the library
(how we operate) with the instance (this application's runbook). Not recommended.

### 5. Documents: a transverse ontology and methodology

**A built-in `document` domain** (like `organisation` and `platform`), available to every namespace:

| Type | What it is |
|---|---|
| `document@DocumentType` | the template: expected sections, which node types and artifact types each section aggregates, its lifecycle, the roles that author and approve it (a library element, instantiated per methodology) |
| `document@Document` | a document of a type about a subject (`about` → any node: an application, a release, a change), with a lifecycle `draft → in_review → approved → published → obsolete` |
| `document@Section` | an ordered part (`contains`), with text and **embeds** → any node, pinned to a version (version-to-version links, ADR 0003) |

- Documents aggregate what methodologies produce: a step's `deliverables` name document types; when the step
  completes, its artifacts and the nodes it wrote are **embedded** in the matching sections (artifacts, which are
  blackboard facts, are materialized as section content). A document version therefore records exactly which versions
  of which elements it presented; the existing document mechanics (ADR 0014: `contains`, guards on the states of
  contained nodes) are reused and generalized from `alm` to every namespace.
- **A transverse `documentation` methodology** holds the processes of documents: author (LLM writer, human editor),
  review, approve, publish, supersede. Any process nests them (`process: documentation/review`), any change can start
  them, and they are not tied to a namespace's methodology. The document types of a methodology (release note, design
  dossier, test report, runbook) are declared once and reused.
- The node index (ADR 0026) indexes documents and their sections, so "find the runbook of the payment service" works
  from the assistant.
- **The existing documentary repository stays the reference.** Processes and steps already point at their reference
  documents (`references`, ADR 0034): a `doc:<key>` document of the graph, or a file of a repository the
  organisation already has, reached through an MCP (`document-repository:<path>`, served by a connector per unit,
  design rule 3). A reference document can be **imported** as a `document@Document` (its sections and embeds
  recognised) or stay external; either way the method (executable) and its description (the reference documents)
  are linked both ways: from a step to the sections that describe it, and from a document to the steps it governs,
  so a change to a reference document can raise a change on the methodology, and the reverse.

### 6. Changes across namespaces (C1)

Documents (§5), operating procedures kept as documents (§4) and methodology updates all mean that one piece of work
touches several namespaces. Proposal: a change keeps **one primary namespace** and may open **companion changes** in
other namespaces (`document`, `methodology`, …): each is a normal change of its own namespace (its own branch, impacts,
reviews, permissions), bound to the primary change, and they are **applied together** (all or none; `merge_pending` on
one holds the others). This keeps ADR 0015 inside each change, and ADR 0016 sub-changes remain the split of work
inside one namespace.

## Phasing

| Phase | Content |
|---|---|
| 1 ✅ | Process progress (§3): `Engine.Progress`, `GetProcessProgress`, shown on the run and the change (nested processes unfold); guidance / checklist / deliverables on steps (§2), the step's context on its human tasks (`HumanTask.context`), in the system prompt of its LLM actions and `{{ .Step }}`, and passed to the sub-agent of an agent or process step |
| 2 | Methods (§1): declaration, selection by context in `process.step`, sdlc `build` / `deploy` / `test` as methods |
| 3 | Roles (§2): methodology roles, role held in a unit, `hasRoleIn`, step resources, task addressees, Access screen |
| 4 | Documents (§5) and companion changes (§6) |
| 5 | Operations (§4): `shipped` stage, standard changes, deployment waves recorded per environment, `EffectiveIn`, operating processes per scope, version pinning |

## Decisions

1. **Methods** (§1): accepted, with the method as the documentary reference and not the actor: a method always names
   its agent, even for a single action (answers open question 1: a method always runs its agent as a sub-agent).
2. **Guidance, roles and responsibilities** (§2): accepted. Roles held in a unit are inherited by the units below it
   (`part_of`), like adapters (design rule 3) — answers open question 2.
3. **Progress** (§3): accepted.
4. **Operations** (§4): the recommended option is accepted — run procedures are delivered with the release and take
   effect from its deployment in each environment; the generic run methodology is pinned per operated scope. A change
   is applied at the approval of its release and **shipped** at the end of its rollout, for the methodologies that
   declare a rollout; for the others, applied is shipped (open question 3). Operating procedures are **documents**
   (`document@Document` of a Runbook type, §5), reviewed and published as such (open question 4).

## Open questions (answered by the decisions above)

1. Is a method always a sub-agent (its own run in the journal), or can a method made of one action run inline as
   today's action steps do?
2. Roles held in a unit: do they inherit down the organisation (a `tech_lead` of a department is `tech_lead` of its
   teams), or must each unit assign its own?
3. Should `apply` move to release approval for every methodology, or only for those that declare a rollout (the
   `shipped` stage being then the end of their process)?
4. Are operating procedures documents (`document@Document` of type Runbook) or nodes of the `alm` domain? Documents
   give review and publication for free; `alm` nodes give finer links to components and interfaces.
