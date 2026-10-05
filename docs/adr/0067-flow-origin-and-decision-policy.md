# ADR 0067 — Flow origin and decision policy out of the graph core

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0009, 0017, 0025, 0032, 0066.

## Context

After ADR 0066 two mechanisms of the core still carried the vocabulary of the engine and of the agents: the flow
branch (`FlowEvent` / `Flow` held `FromStep`, `Execution`, `Process`, `Reason`, `StaleExecutions`, and `OpenFlow`
wrote a `guidance` artifact the engine reads) and the decision point (`Threshold`, `MaxRounds`, `Rounds`, `Decider`,
the deadline, and a replay that settled a point when `Human || Confidence >= Threshold` and escalated it after
`MaxRounds`). The graph core keeps mechanism; what a relaunch of a step records, and how sure an agent must be to
settle a point alone, are policy.

## Decision

Greenfield: no tolerant decoding of old logs, no compatibility fields (the change logs are discarded with the
database); the proto removes the fields (numbers reserved) and the schema is unchanged (the events are JSON in the
log).

1. **Flow.** `FlowEvent` / `Flow` keep `Parent`, `ForkAfter`, `Stale` (items), `By`, `Option`, and gain
   `StaleRuns []string` and `Origin map[string]any`.
   - `StaleRuns` (formerly `StaleExecutions`) are opaque producer ids, the `Execution` of the change impacts and node
     versions: the graph only compares them, and keeps the rule built on them (a flow that invalidated nothing joins
     the change branch, else its versions are copied; which versions and impacts an adoption supersedes; which flows
     compete). The word "execution" is gone from the flow; the name is `StaleRuns` rather than `Stale`, which already
     lists the stale items.
   - `Origin` is an opaque record of the opener: stored and returned, never read by the graph. The engine writes
     `{step, execution, process, reason}` for a relaunch; the option mechanism writes none (its hypothesis is
     `Option.Hypothesis`).
   - `OpenFlowRequest` loses `FromStep`, `Execution`, `Process`, `Reason`, `Guidance`, `By` and gains `Origin` and
     `Items []domain.ChangeItem`, written on the new flow at its creation (given an id, the flow, the time and the
     accepted status when unset; a flow, transition or decision-point item is refused, those have their own
     operations). The engine builds the guidance artifact itself (`engine.GuidanceType`, `Relaunch`); the graph names
     no `"guidance"`.
2. **Decision policy.** `DecisionEvent` / `DecisionPoint` lose `Decider`, `Threshold`, `MaxRounds`, `Deadline`,
   `Rounds` and gain `Policy map[string]any` (the values the opener gave, as the policy resolved them: opaque to the
   graph) and, on the point, `HumanOnly` (only a person may rule it now: by design or escalated; `Escalation` keeps
   the reason). The replay asks a `domain.DecisionPolicy`:
   - `Open(given, now) (values, error)`: defaults, validation, values relative to now (a deadline becomes absolute
     here, so that the replay stays pure);
   - `Fold(p *DecisionPoint, ev DecisionEvent) (settled bool)`: after the core recorded a rule or a ratify, says
     whether it settles the point; may update `p.Policy` (the replayed copy: the count of rounds lives there);
   - `Reserve(p, now) (humanOnly bool, escalation string)`: asked of the points not decided.
   The core keeps open / rule / answer / ratify, who ruled, the statuses (`open`, `blocked`, `ratifying`, `escalated`,
   `decided`), the lifecycle gate that consumes a decided point and the selection of an option by a decided point.
   `Graph.DecisionPolicy` is the policy of the graph; unset, `domain.DefaultDecisionPolicy`: a decided ruling of
   anyone settles the point, an accepted ratification settles it, nothing is reserved to a person, policy values are
   refused.
   **The policy is a parameter, not a registry**: `Change.DecisionPointsAt(now, policy)` and `DecisionPoint(id, now,
   policy)`. The callers are the graph's own (it holds the policy), so the replay stays a pure function of the log,
   the moment and the policy, with no global state and tests that run in parallel with different policies. (Item kinds
   of ADR 0065 are a registry because `ChangeItem.Validate` has no graph in hand; the replay does.)
3. **`pkg/decision`** is the platform's policy, with the same math, defaults and escalation texts as before: values
   `decider` (`agent` / `human`), `threshold` (0.7), `maxRounds` (3; negative: no limit), `maxDuration` (a duration
   that `Open` turns into `deadline`), kept `rounds`; a decided ruling settles when `Human || Confidence >=
   threshold`, an undecidable ruling or a refused ratification is a round, 3 rounds or the deadline escalate, a
   human decider reserves the point without escalating it. The composition roots plug it explicitly
   (`g.DecisionPolicy = decision.Policy{}` in `cmd/graph`, `cmd/goap-dev`, and the tests that exercise it).
   `pkg/graph` and `pkg/domain` must not import it (`pkg/layering`), nor name the vocabulary above
   (`TestCoreNamesNoEnginePolicy`).
4. **Callers.** The CEL activation of a decision point exposes `policy` (a map: whole numbers as ints) and `humanOnly`
   instead of `decider` / `threshold` / `rounds` / `maxRounds`; `status` and `escalation` are unchanged, so the
   `decisions` library keeps its conditions as written. The DSL `ctx.openDecision(question, spec)` keeps its spec: `options`
   and `criteria` are the point's, every other key is a policy value. The `goap-change` `decision` tool keeps its
   arguments and hands them over as policy values. RPC: `OpenDecisionRequest.policy`, `DecisionPoint.policy` /
   `human_only`, `FlowEvent` / `Flow` / `OpenFlowRequest` `origin`, `stale_runs`, `OpenFlowRequest.items`.

## Consequences

- A use case other than the engine can open flows with its own origin and its own items, and decide points under its
  own policy, without a change to the graph.
- A flow of an option has no `Reason`; the web reads the relaunch's step, process and reason from `Flow.origin`.
- The policy values cross the wire as JSON numbers: `pkg/decision` reads any numeric encoding; expressions see whole
  numbers as ints.
- PostgreSQL: no schema change (events live in the JSON of the log); PG tests not executed here.
