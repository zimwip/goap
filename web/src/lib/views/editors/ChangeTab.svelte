<script lang="ts">
  // Change tab: change items and applying to the baseline.
  import {
    graph,
    engine,
    errorMessage,
    isNotFound,
    formatDate,
    shortId,
    type Baseline,
    ITEM_SUPERSEDED,
    type ChangeItem,
    type Change,
    type Flow,
    type DecisionPoint,
    type Lifecycle,
    type BoardIssue,
    type GraphNode,
    type LifecycleTransition,
    type NodeRef,
    type Resolution,
    type UiTool,
    type StartingPoint,
    type StartingPointsResponse,
  } from '../../api';
  import { tick, untrack } from 'svelte';
  import { applicable, followChangeProject, loadApplicable } from '../../stores/project.svelte';
  import { registerAssist, revealTarget, type ToolImpl } from '../../assist/registry.svelte';
  import { changeSummary, impactEntities, stepEntities, transitionEntities, type ImpactFacts } from '../../assist/changeScreen';
  import { recordAction } from '../../assist/recorder';
  import { loadMoveOffer, moveChangeTo } from '../../changeMove';
  import { availableTransitions, decisionLabel, findLifecycle, movable as lifecycleMovable, pickableDecisions, resolveCall, runTransition, type Refusal, type TransitionOffer } from '../../changeTransition';
  import ChangeTransitions from '../../components/ChangeTransitions.svelte';
  import MoveChange from '../../components/MoveChange.svelte';
  import { movable } from '../../changeProject';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import ChangeAudit from '../../components/ChangeAudit.svelte';
  import { artifactsOf, decisionsByItem, rawItems } from '../../artifacts';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import ChangeLifecycle from '../../components/ChangeLifecycle.svelte';
import ChangeLifecycleView from '../../components/ChangeLifecycleView.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import { lifecycleRows, reopenable, nodeTypeNames, lifecycleResolver, loadPosts, awaitingReview, acceptAllProposed, writeNodeInChange, removeFromChange, type PostVersions, type LifecycleRow } from '../../lifecycle';
  import { loadTypes, typeCatalog } from '../../stores/types.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openNode } from '../../nodeEditors';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { stamp, keyOf, throttled } from '../../flux/signals.svelte';
  import { viewBaseline } from '../../stores/baselineTool.svelte';
  import { namespaceOf } from '../../namespace';
  import { processes } from '../../stores/live.svelte';
  import ProcessProgress from '../../components/ProcessProgress.svelte';
  import PossibleSteps from '../../components/PossibleSteps.svelte';
  import { confirmText, pointById, showStartingPoints, startable, startableIds, startRequest } from '../../startingPoints';
  import FlowGraph from '../../components/FlowGraph.svelte';
  import FlowActions from '../../components/FlowActions.svelte';
  import FlowBranchInfo from '../../components/FlowBranchInfo.svelte';
  import { flowStepNumber } from '../../flowChain';
  import { processOfFlow } from '../../flowDecision';
  import BoardIssueList from '../../components/BoardIssueList.svelte';
  import CompareDialog from '../../components/CompareDialog.svelte';
  import ChangeDecisions from '../../components/ChangeDecisions.svelte';
  import ChangeRisks from '../../components/ChangeRisks.svelte';
  import ChangeCriticality from '../../components/ChangeCriticality.svelte';
  import ChangeVerification from '../../components/ChangeVerification.svelte';
  import ChangeDerogations from '../../components/ChangeDerogations.svelte';
  import ReviewPanel from '../../components/ReviewPanel.svelte';
  import SubChangeSync from '../../components/SubChangeSync.svelte';
  import SubChangeHistory from '../../components/SubChangeHistory.svelte';
  import { foldReviews } from '../../reviews';
  import { riskRegister, liveRisk } from '../../risks';
  import { verifications, derogationRegister, openDerogation } from '../../verification';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import ScopeBar from '../../components/ScopeBar.svelte';
  import MergeResolver from '../../components/MergeResolver.svelte';
  import { MAIN_SCOPE, candidatesByOption, scopeColor, scopeName, scopeTabTitle, scopeWritable } from '../../changeScope';

  import NotFound from '../../shell/NotFound.svelte';
  import { loadMethodology, published } from '../../stores/catalog.svelte';
  import { goalInfo } from '../../changeGoal';

  let { tab }: { tab: Tab } = $props();

  let change = $state<Change | undefined>();
  // what its methodology says of the goal of the change (ADR 0096); a goal the methodology no longer declares is shown
  const goalNote = $derived(goalInfo(published.get(change?.methodology ?? ''), change?.goal ?? ''));
  $effect(() => {
    if (change?.goal && change.methodology) void loadMethodology(change.methodology);
  });
  let nodes = $state<GraphNode[]>([]);
  let attached = $state<NodeRef[]>([]);
  let posts = $state<PostVersions>(new Map());
  let extraNodes = $state<string[]>([]);
  let moving = $state('');
  /** the Compare & decide dialog, opened from the scope bar */
  let compareOpen = $state(false);
  let pane = $state(untrack(() => tab.params.pane) || 'overview');
  $effect(() => {
    tab.params.pane = pane;
  });
  // reopening this tab (already mounted) with different params — e.g. from a process's "Audit" link — should still
  // jump there, so also sync the other way
  $effect(() => {
    if (tab.params.pane && tab.params.pane !== pane) pane = tab.params.pane;
  });
  /** the process the Audit pane is restricted to, opened from elsewhere (e.g. a process's "Audit" link) */
  let auditProcess = $state(untrack(() => tab.params.process) || '');
  $effect(() => {
    tab.params.process = auditProcess;
  });
  $effect(() => {
    if (tab.params.process !== undefined && tab.params.process !== auditProcess) auditProcess = tab.params.process;
  });
  /** the action run the Audit pane is restricted to, opened from elsewhere (e.g. an item's producer link) */
  let auditRun = $state(untrack(() => tab.params.run) || '');
  $effect(() => {
    tab.params.run = auditRun;
  });
  $effect(() => {
    if (tab.params.run !== undefined && tab.params.run !== auditRun) auditRun = tab.params.run;
  });
  let loading = $state(false);
  let error = $state('');
  let missing = $state(false);
  /** the flow the editor looks at: the main flow or one option (ADR 0032 §6); local to the editor, it does not
   * move the active option. The impacts and items panes show the change as that flow sees it. */
  let scope = $state(untrack(() => tab.params.scope) || '');
  $effect(() => {
    tab.params.scope = scope;
  });
  /** the change as the scope sees it: its change impacts (with the posts of that flow) and items */
  let view = $state<Change | undefined>();

  let subs = $state<Change[]>([]);
  /** parent chain, the root first */
  let ancestors = $state<Change[]>([]);
  let flows = $state<Flow[]>([]);
  let boardIssues = $state<BoardIssue[] | null>(null);
  let checking = $state(false);
  let checkError = $state('');

  async function checkBoard() {
    if (!selected) return;
    checking = true;
    checkError = '';
    try {
      boardIssues = (await graph.validateBoard(selected, scope || MAIN_SCOPE)).issues ?? [];
    } catch (e) {
      checkError = errorMessage(e);
    } finally {
      checking = false;
    }
  }
  let splitting = $state(false);
  /** the branch a change on its own branch merges into (its parent branch) */
  let mergeInto = $state('');
  // editing the definition of the change (title, intent) and abandoning it
  let defining = $state(false);
  let defTitle = $state('');
  let defIntent = $state('');
  let defBusy = $state(false);

  let baselineName = $state('');
  let applying = $state(false);
  let applied = $state<Baseline | undefined>();
  let moveOpen = $state(false);
  // the lifecycle definition the change follows, its decision points and the transition being taken (ADR 0058)
  let lcDef = $state<Lifecycle>();
  let lcDomain = $state('');
  let lcFailure = $state('');
  let lcLoading = $state(false);
  let decisionPoints = $state<DecisionPoint[]>([]);
  let transitioning = $state('');
  let transitionRefusal = $state<{ transition: string; refusal: Refusal }>();

  const selected = $derived(tab.params.id ?? '');

  /** the baseline `nodes` was read from */
  let nodesOf = '';

  /** The parents of a sub-change, the root first. */
  async function loadAncestors(c: Change | undefined, signal?: AbortSignal): Promise<Change[]> {
    const chain: Change[] = [];
    for (let p = c?.parentId; p && chain.length < 16; ) {
      const parent = (await graph.getChange(p, signal)).change;
      if (!parent) break;
      chain.unshift(parent);
      p = parent.parentId;
    }
    return chain;
  }

  async function load(id: string, signal?: AbortSignal) {
    loading = true;
    error = '';
    try {
      const c = (await graph.getChange(id, signal)).change;
      change = c;
      void followChangeProject(c);
      if (!baselineName) baselineName = c?.title ? `${c.title}` : `change-${shortId(id)}`;
      // independent reads go out together; a baseline never changes, so the one already read is kept (every event of the
      // change loads again: re-reading its whole graph each time was the heaviest call of the screen)
      const baselineId = c?.baselineId ?? '';
      const [baseNodes, subList, chain] = await Promise.all([
        !baselineId ? Promise.resolve([] as typeof nodes) : baselineId === nodesOf && nodes.length ? Promise.resolve(nodes) : graph.getBaselineGraph(baselineId, signal).then((g) => g.nodes ?? []),
        graph.listSubChanges(id, signal).then((r) => r.changes ?? []),
        loadAncestors(c, signal),
        graph.listFlows(id, signal).then((r) => (flows = r.flows ?? [])),
      ]);
      nodes = baseNodes;
      nodesOf = baselineId;
      subs = subList;
      ancestors = chain;
      // no scope yet, or one decided since: the option agents work on, else the main flow
      const opts = flows.filter((f) => f.option);
      if (!scope || (scope !== MAIN_SCOPE && !opts.some((o) => o.id === scope))) scope = opts.find((o) => o.active)?.id ?? MAIN_SCOPE;
      await loadScope(id, scope, signal);
    } catch (e) {
      if (!signal?.aborted) {
        missing = isNotFound(e);
        error = errorMessage(e);
      }
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  /** Loads the change as the scope sees it. */
  async function loadScope(id: string, sc: string, signal?: AbortSignal) {
    loadedScope = sc;
    const bb = await graph.getBlackboard(id, sc || MAIN_SCOPE, signal);
    const v = bb.change;
    decisionPoints = bb.decisionPoints ?? [];
    const ps = await loadPosts(v?.nodes ?? [], true, { changeId: id, flow: sc || MAIN_SCOPE });
    if (signal?.aborted || sc !== scope) return;
    view = v;
    posts = ps;
    attached = (v?.nodes ?? []).filter((n) => n.pre?.id && !n.superseded).map((n) => n.pre!);
    boardIssues = null;
  }

  // switching the scope reloads what the scoped panes show
  let loadedScope = '';
  $effect(() => {
    const sc = scope;
    const id = untrack(() => change?.id);
    if (!id || !sc || sc === loadedScope) return;
    loadedScope = sc;
    const ctrl = new AbortController();
    loadScope(id, sc, ctrl.signal).catch((e) => {
      if (!ctrl.signal.aborted) error = errorMessage(e);
    });
    return () => ctrl.abort();
  });

  $effect(() => {
    const id = selected;
    change = undefined;
    view = undefined;
    loadedScope = '';
    attached = [];
    subs = [];
    ancestors = [];
    flows = [];
    defining = false;
    extraNodes = [];
    applied = undefined;
    baselineName = '';
    if (!id) return;
    const ctrl = new AbortController();
    load(id, ctrl.signal);
    return () => ctrl.abort();
  });

  // what the platform stream says touched this change (by anyone: another user, an agent, a process): read it again
  // (a running agent journals many entries a second, each an event: at most one reload per second)
  let reloading: AbortController | undefined;
  const reloadSoon = throttled(() => {
    const id = selected;
    if (!id || change?.id !== id) return;
    reloading?.abort();
    reloading = new AbortController();
    void load(id, reloading.signal);
  }, 1000);
  $effect(() => {
    const id = selected;
    if (!id || !stamp(keyOf.change(id))) return;
    untrack(reloadSoon.call);
    return reloadSoon.cancel;
  });
  // another change, or leaving: what was being read is dropped
  $effect(() => {
    void selected;
    return () => reloading?.abort();
  });

  // the steps possible now towards the goal of the change (ADR 0097): read again when the platform stream says the change
  // moved (a run journals many entries a second: at most once per second), no polling
  let startPoints = $state<StartingPointsResponse | undefined>();
  let pointsError = $state('');
  let startingId = $state('');
  const showSteps = $derived(showStartingPoints(change));
  // a change with no methodology of its own: the one chosen among the project's to see its possible steps
  let pointsMethodology = $state('');
  const methodologyChoices = $derived(change && !change.methodology ? (applicable.names ?? []) : []);
  $effect(() => {
    if (change && !change.methodology) untrack(() => void loadApplicable());
  });
  $effect(() => {
    void selected;
    pointsMethodology = '';
  });
  function choosePointsMethodology(name: string) {
    pointsMethodology = name;
    pointsSoon.call();
  }
  let readingPoints: AbortController | undefined;
  const pointsSoon = throttled(() => {
    const id = selected;
    if (!id) return;
    readingPoints?.abort();
    const ctrl = (readingPoints = new AbortController());
    engine
      .listStartingPoints(id, ctrl.signal, pointsMethodology)
      .then((r) => {
        if (ctrl.signal.aborted) return;
        startPoints = r;
        pointsError = '';
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) pointsError = errorMessage(e);
      });
  }, 1000);
  $effect(() => {
    const id = selected;
    if (!id || !showSteps) {
      startPoints = undefined;
      return;
    }
    void stamp(keyOf.change(id));
    untrack(pointsSoon.call);
    return () => {
      pointsSoon.cancel();
      readingPoints?.abort();
    };
  });

  /** Starts a possible step with the launch the engine gave it; the scheduler plans its actions. `assisted`: the proposal card was the confirmation. */
  async function startPoint(p: StartingPoint, assisted = false): Promise<string | undefined> {
    if (!startable(p)) return `The step ${p.id} cannot be started now.`;
    if (!assisted && !(await confirmDialog({ title: 'Start step', message: confirmText(p, change?.goal ?? ''), confirmLabel: 'Start' }))) return;
    startingId = p.id;
    try {
      await engine.startProcess(startRequest(p));
      notify(`Started “${p.name}”.`, 'ok');
      recordAction(`started the step ${p.id}`);
      pointsSoon.call();
    } catch (e) {
      const m = errorMessage(e);
      if (!assisted) error = m;
      return m;
    } finally {
      startingId = '';
    }
  }

  // the definition of the lifecycle the change follows (re-read when the lifecycle or the namespace changes)
  $effect(() => {
    const name = change?.lifecycle ?? '';
    const ns = change?.namespace ?? '';
    lcDef = undefined;
    lcFailure = '';
    if (!name) return;
    const ctl = new AbortController();
    lcLoading = true;
    findLifecycle(name, ns, ctl.signal)
      .then((r) => {
        if (r) {
          lcDef = r.lc;
          lcDomain = r.domain;
        } else lcFailure = `No domain defines the lifecycle “${name}”.`;
      })
      .catch((e) => {
        if (!ctl.signal.aborted) lcFailure = errorMessage(e);
      })
      .finally(() => {
        if (!ctl.signal.aborted) lcLoading = false;
      });
    return () => ctl.abort();
  });
  const offers = $derived(lifecycleMovable(change) ? availableTransitions(lcDef, change?.state) : []);
  const pickable = $derived(pickableDecisions(decisionPoints, change?.items));

  /** Takes a transition of the lifecycle: the button (with its confirmation) and the assistant (its card confirmed). */
  async function takeTransition(offer: TransitionOffer, decision?: DecisionPoint, skipConfirm = false): Promise<string | undefined> {
    transitioning = offer.name;
    transitionRefusal = undefined;
    error = '';
    try {
      const r = await runTransition(
        { confirm: confirmDialog, call: (id, t, d) => graph.transitionChange(id, t, d), refresh: async () => {
            if (change?.id) await load(change.id);
          },
          notify },
        { change: change ?? {}, offer, decision, skipConfirm },
      );
      if (r.ok) return undefined;
      if (r.cancelled) return r.message;
      if (r.refusal) transitionRefusal = { transition: offer.name, refusal: r.refusal };
      else error = r.message;
      return r.message;
    } finally {
      transitioning = '';
    }
  }

  /** status of an item on the log: base status, then what the flow branches make of it */
  function effectiveStatus(i: ChangeItem): string | undefined {
    const own = i.flow ? flows.find((f) => f.id === i.flow) : undefined;
    if (own?.status === 'open') return 'candidate';
    if (own?.status === 'discarded') return 'rejected';
    for (const f of flows) {
      if (!f.stale?.includes(i.id ?? '')) continue;
      if (f.status === 'adopted') return 'superseded';
      if (f.status === 'open' && (i.status === 'proposed' || i.status === 'accepted')) return 'stale';
    }
    return i.status;
  }
  // flow events are part of the log but not shown as items
  const items = $derived((view?.items ?? []).filter((i) => i.kind !== 'flow' && i.kind !== 'decision_point').map((i) => ({ ...i, status: effectiveStatus(i) })));
  const flowProcess = (f: Flow) => processOfFlow(f);
  // options are flows opened as hypotheses (ADR 0032 §6): they have a pane of their own
  const options = $derived(flows.filter((f) => f.option));
  const relaunches = $derived(flows.filter((f) => !f.option));
  // decision points that are not decided yet (ADR 0009 §4), replayed from the facts of the main flow
  const pendingDecisions = $derived.by(() => {
    const threshold = new Map<string, number>();
    const decided = new Set<string>();
    for (const it of change?.items ?? []) {
      const e = it.decisionEvent;
      if (it.kind !== 'decision_point' || !e || it.flow) continue;
      if (e.op === 'open') threshold.set(it.id ?? '', e.policy?.threshold ?? 0);
      else if (e.op === 'rule' && e.outcome === 'decided' && (e.human || (e.confidence ?? 0) >= (threshold.get(e.point ?? '') ?? 1))) decided.add(e.point ?? '');
      else if (e.op === 'ratify' && e.accept) decided.add(e.point ?? '');
    }
    const opened = [...threshold.keys()];
    return opened.filter((id) => !decided.has(id)).length;
  });
  /** the badge of a flow: open flows that compete cannot be adopted any more */
  const flowBadge = (f: Flow) => (f.status === 'open' && f.competesWith?.length ? 'competing' : f.status);
  const artifacts = $derived(artifactsOf(items));
  // item-level decisions are shown on the artifact they concern; the others stay in the Audit pane
  const decisionsOn = $derived(decisionsByItem(items));
  const raw = $derived(rawItems(items));

  // applied, or applied on its own branch and waiting for its merge: nothing to apply any more
  const isApplied = $derived(change?.status === 'applied' || change?.status === 'committed');
  const closed = $derived(change?.status === 'applied' || change?.status === 'abandoned');
  // a sub-change leaves no baseline: it is integrated into its parent (ADR 0081), so it has no baseline to name
  const isSub = $derived(!!change?.parentId);
  const unnamed = $derived(!isSub && !baselineName.trim());
  void loadTypes();
  const lcRows = $derived(lifecycleRows(typeCatalog.cat, nodes, attached, view?.nodes ?? [], posts, extraNodes, true));
  // the scope: what it shows, whether it can be edited, its colour
  const writable = $derived(scopeWritable(options, scope, closed));
  const scopeLabel = $derived(scopeName(options, scope));
  const scopeTint = $derived(scopeColor(options, scope));
  const candidates = $derived(candidatesByOption(change?.nodes ?? []));
  const mainImpacts = $derived((change?.nodes ?? []).filter((n) => !n.flow && !n.superseded).length);
  // a change creates and modifies the nodes of its namespace (ADR 0015 §2)
  const lcCandidates = $derived(reopenable(nodes, lcRows, change?.namespace ?? ''));
  const typeNames = $derived(nodeTypeNames(typeCatalog.cat, change?.namespace ?? ''));
  const lifecycleOf = $derived(lifecycleResolver(typeCatalog.cat));
  const takenKeys = $derived([
    ...nodes.map((n) => n.key ?? ''),
    ...(view?.nodes ?? []).filter((n) => n.intent === 'created' && !n.superseded && n.review !== 'rejected').map((n) => n.key ?? ''),
  ]);
  // the graph refuses to land a change with an impact awaiting its review (ADR 0079: edits never review)
  const awaiting = $derived(awaitingReview(view?.nodes ?? [], true));
  const stuckNotLandable = $derived(lcRows.some((r) => r.lifecycle && !r.landable));
  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview', badge: stuckNotLandable ? '!' : undefined },
    { id: 'impacts', label: 'Impacts', tint: scopeTint, title: scopeTabTitle(options, scope), badge: awaiting.length ? `${awaiting.length} to review` : view?.nodes?.length || undefined },
    { id: 'reviews', label: 'Reviews', tint: scopeTint, title: scopeTabTitle(options, scope), badge: foldReviews(change?.items ?? []).filter((r) => r.status === 'open' && (r.flow ?? '') === (scope === MAIN_SCOPE ? '' : scope)).length || undefined },
    { id: 'artifacts', label: 'Artifacts', tint: scopeTint, title: scopeTabTitle(options, scope), badge: artifacts.length || undefined },
    { id: 'decisions', label: 'Decisions', badge: pendingDecisions || undefined },
    { id: 'risks', label: 'Risks & actions', badge: riskRegister(change?.items ?? []).filter(liveRisk).length || undefined },
    { id: 'verification', label: 'Verification', badge: verifications(change?.items ?? []).filter((v) => v.open).length || undefined },
    { id: 'derogations', label: 'Derogations', badge: derogationRegister(change?.items ?? []).filter(openDerogation).length || undefined },
    { id: 'changes', label: 'Changes', badge: subs.length + ancestors.length || undefined },
    { id: 'audit', label: 'Audit' },
  ]);

  /** Writes a node in this change through its change impact (the server checks the state). */
  async function write(label: string, target: { pre?: NodeRef; key?: string; type?: string }, w: { props?: Record<string, unknown>; state?: string }, rationale: string): Promise<boolean> {
    if (!change?.id) return false;
    moving = label;
    error = '';
    try {
      await writeNodeInChange(change.id, view?.nodes ?? [], target, w, rationale, scope || MAIN_SCOPE);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  /** Writes new values for some properties (and possibly the state) of a node of this change. */
  async function edit(row: LifecycleRow, patch: Record<string, unknown>, state?: string): Promise<boolean> {
    if (row.created) return write(`${row.node.id}:edit`, { key: row.node.key, type: row.node.type }, { props: patch, state }, `edit ${row.node.key}`);
    if (!row.node.id) return false;
    return write(`${row.node.id}:edit`, { pre: { id: row.node.id, version: row.node.version } }, { props: patch }, `edit ${row.node.key}`);
  }

  /** Declares that the change (on the scope) impacts a node of its baseline, and why: a planned change impact. */
  async function addImpact(id: string, rationale = '') {
    const n = nodes.find((x) => x.id === id);
    if (!change?.id || !n?.id) return;
    moving = `${id}:add`;
    error = '';
    try {
      await graph.proposeImpact(change.id, [{ intent: 'modified', pre: { id: n.id, version: n.version }, rationale: rationale || `work on ${n.key}`, flow: scope || MAIN_SCOPE }]);
      await load(change.id);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      moving = '';
    }
  }

  /** Accepts or rejects the change impact of a row, on the scope. */
  async function reviewRow(row: LifecycleRow, accept: boolean, comment: string): Promise<boolean> {
    if (!change?.id || !row.impact?.id) return false;
    moving = `${row.node.id}:review`;
    error = '';
    try {
      await graph.impactNodeReview(change.id, row.impact.id, accept, comment, scope || MAIN_SCOPE);
      recordAction(`${accept ? 'accepted' : 'rejected'} the impact of ${row.node.key}`);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  /** Accepts every impact awaiting its review, on the scope: an explicit button, one review each with the given comment. */
  async function acceptAll(comment: string): Promise<boolean> {
    if (!change?.id) return false;
    moving = 'review-all';
    error = '';
    try {
      await acceptAllProposed(change.id, view?.nodes ?? [], comment, scope || MAIN_SCOPE, true);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }

  /** Creates a node: identity and type only. */
  async function createNode(key: string, type: string, state: string): Promise<boolean> {
    const born = lifecycleOf(type) && state && state !== lifecycleOf(type)?.initial ? state : undefined;
    return write('create', { key, type }, { props: {}, state: born }, `create ${key}`);
  }

  /** Takes a node out of the change: its draft is dropped (a node the change creates goes away); refused
   * once its version has landed (reject it instead, ADR 0076, 0079). */
  async function removeNode(row: LifecycleRow): Promise<boolean> {
    if (!change?.id || !row.impact?.id) return false;
    moving = `${row.node.id}:remove`;
    error = '';
    try {
      await removeFromChange(change.id, row.impact, scope || MAIN_SCOPE);
      await load(change.id);
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      moving = '';
    }
  }


  async function move(row: LifecycleRow, t: LifecycleTransition) {
    if (!row.node.id) return;
    await write(`${row.node.id}:${t.name}`, { pre: { id: row.node.id, version: row.node.version } }, { state: t.to }, `${t.name} ${row.node.key}`);
  }

  const ownBranch = $derived((change?.branch ?? '').startsWith('change-'));
  const openSubs = $derived(subs.filter((s) => s.status !== 'applied' && s.status !== 'abandoned'));

  async function split() {
    if (!change?.id) return;
    splitting = true;
    error = '';
    try {
      const made = (await graph.splitChange(change.id)).changes ?? [];
      await load(change.id);
      notify(made.length ? `${made.length} sub-change(s) created.` : 'No new sub-change: every owning unit already has one.', 'ok');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      splitting = false;
    }
  }

  $effect(() => {
    const ch = change;
    mergeInto = '';
    if (ch?.status !== 'committed' || !ch.branch) return;
    graph
      .getBranch(namespaceOf(ch.namespace), ch.branch)
      .then((r) => (mergeInto = r.branch?.parent || 'main'))
      .catch(() => (mergeInto = 'main'));
  });

  async function merge(resolutions: Record<string, Resolution>): Promise<boolean> {
    if (!change?.id) return false;
    await graph.mergeChange(change.id, resolutions);
    await load(change.id);
    notify('Change merged.', 'ok');
    return true;
  }

  function startDefine() {
    defTitle = change?.title ?? '';
    defIntent = change?.intent ?? '';
    defining = true;
  }

  async function define(patch: { title?: string; intent?: string; status?: string }, done: string): Promise<boolean> {
    if (!change?.id) return false;
    defBusy = true;
    error = '';
    try {
      await graph.updateChange(change.id, patch);
      defining = false;
      await load(change.id);
      notify(done, 'ok');
      recordAction(patch.status === 'abandoned' ? 'abandoned the change' : 'edited the title or intent of the change');
      return true;
    } catch (e) {
      error = errorMessage(e);
      return false;
    } finally {
      defBusy = false;
    }
  }

  const abandon = async () => {
    if (
      await confirmDialog({
        message: `Abandon “${change?.title}”? Its sub-changes are abandoned too and its branch is closed; nothing it wrote lands.`,
        danger: true,
      })
    )
      void define({ status: 'abandoned' }, 'Change abandoned.');
  };

  async function apply() {
    if (!change?.id) return;
    applying = true;
    error = '';
    try {
      const parent = change.parentId;
      applied = (await graph.applyChange(change.id, baselineName.trim())).baseline;
      await load(change.id);
      // a sub-change leaves no baseline: it is integrated into its parent (ADR 0081, 0082)
      if (parent) applied = undefined;
      notify(parent ? `Integrated into parent change ${shortId(parent)}.` : `Baseline ${applied?.name || shortId(applied?.id)} created.`, 'ok');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      applying = false;
    }
  }

  function markdownOf(i: ChangeItem): string | undefined {
    const md = i.data?.['markdown'];
    return typeof md === 'string' ? md : undefined;
  }

  const related = $derived([...processes.values()].filter((p) => p.changeId && p.changeId === selected));

  /** Switches to the Audit pane, optionally restricted to one action run. */
  function openAudit(run = '') {
    pane = 'audit';
    auditProcess = '';
    auditRun = run;
  }

  function provenance(i: ChangeItem): string {
    const parts = [i.producedBy ? `Produced by ${i.producedBy}` : 'Unknown producer'];
    if (i.execution) parts.push(`execution ${shortId(i.execution)} — open in the audit trail`);
    if (i.supersedes?.length) parts.push(`supersedes ${i.supersedes.map(shortId).join(', ')}`);
    if (i.status === ITEM_SUPERSEDED) parts.push('superseded by a more recent item');
    return parts.join(' · ');
  }

  function openBaseline(id: string | undefined) {
    if (id) void viewBaseline(id, change?.namespace ?? '');
  }


  // --- what the assistant sees and may do on this screen (ADR 0092) -----------------------------------------------
  /** the impact pointed at (by the person or the assistant), outlined in the Impacts pane */
  let selectedImpact = $state('');
  /** the review status the Impacts pane shows: all | proposed | accepted | rejected */
  let impactFilter = $state('all');
  const shownRows = $derived(impactFilter === 'all' ? lcRows : lcRows.filter((r) => (r.impact?.review || 'proposed') === impactFilter));
  const impactFacts = $derived<ImpactFacts[]>(
    lcRows.flatMap((r) =>
      r.impact?.id && !r.impact.superseded
        ? [{ id: r.impact.id, key: r.node.key ?? '', type: r.node.type, intent: r.impact.intent, review: r.impact.review, state: r.effective || undefined, landable: r.landable, created: !!r.created }]
        : [],
    ),
  );
  const rowOfImpact = (id: unknown) => lcRows.find((r) => r.impact?.id === id && !r.impact?.superseded);
  /** the ids offered as an enum when they fit the server's cap of 30, else a plain string */
  const withIds = (name: string, ids: () => string[]) => (base: UiTool): UiTool => {
    const list = ids();
    const impactId = list.length && list.length <= 30 ? { type: 'enum' as const, enum: list, description: 'the impact id' } : { type: 'string' as const, description: 'the impact id' };
    return { ...base, name, args: { ...base.args, properties: { ...base.args?.properties, impactId } } };
  };
  const awaitingIds = () => impactFacts.filter((i) => !i.review || i.review === 'proposed').map((i) => i.id);
  const reviewable = (r: ReturnType<typeof rowOfImpact>) => !!r?.impact && (r.impact.review === 'proposed' || !r.impact.review);

  const assistTools: ToolImpl[] = [
    {
      name: 'transition_change',
      enabled: () => !!change && !defining && offers.length > 0,
      describe: (base) => {
        const names = offers.map((o) => o.name);
        const transition = names.length && names.length <= 30 ? { type: 'enum' as const, enum: names, description: 'the transition name' } : { type: 'string' as const, description: 'the transition name' };
        return { ...base, args: { ...base.args, properties: { ...base.args?.properties, transition } } };
      },
      run: async (a) => {
        const c = resolveCall(offers, pickable, a);
        if (typeof c === 'string') return c;
        return takeTransition(c.offer, c.decision, true);
      },
    },
    {
      name: 'start_step',
      enabled: () => !!change && !closed && startableIds(startPoints).length > 0,
      describe: (base) => {
        const ids = startableIds(startPoints);
        const step = ids.length <= 30 ? { type: 'enum' as const, enum: ids, description: 'the step id' } : { type: 'string' as const, description: 'the step id' };
        return { ...base, args: { ...base.args, properties: { ...base.args?.properties, step } } };
      },
      run: async (a) => {
        const p = pointById(startPoints, a.step);
        if (!p) return `The step ${String(a.step)} is not possible now on this change.`;
        return startPoint(p, true);
      },
    },
    {
      name: 'select_impact',
      enabled: () => !!change && impactFacts.length > 0,
      describe: withIds('select_impact', () => impactFacts.map((i) => i.id)),
      targetOf: (a) => `impact:${a.impactId}`,
      run: async (a) => {
        const r = rowOfImpact(a.impactId);
        if (!r) return `The impact ${String(a.impactId)} is not in this change.`;
        pane = 'impacts';
        if (impactFilter !== 'all' && (r.impact?.review || 'proposed') !== impactFilter) impactFilter = 'all';
        selectedImpact = String(a.impactId);
        await tick();
        revealTarget(`impact:${selectedImpact}`);
      },
    },
    {
      name: 'open_impact',
      enabled: () => !!change && impactFacts.some((i) => !i.created || !!rowOfImpact(i.id)?.impact?.post?.id),
      describe: withIds('open_impact', () => impactFacts.map((i) => i.id)),
      targetOf: (a) => `impact:${a.impactId}`,
      run: (a) => {
        const r = rowOfImpact(a.impactId);
        if (!r) return `The impact ${String(a.impactId)} is not in this change.`;
        if (r.created && !r.impact?.post?.id) return 'This node has no draft to open yet.';
        selectedImpact = String(a.impactId);
        void openNode({ id: r.impact?.post?.id ?? r.node.id ?? '', key: r.node.key ?? '' }, { pin: true, change: change?.id ?? '', flow: scope || MAIN_SCOPE });
      },
    },
    {
      name: 'filter_impacts',
      enabled: () => !!change,
      run: (a) => {
        pane = 'impacts';
        impactFilter = String(a.review);
      },
    },
    {
      name: 'review_impact',
      enabled: () => !!change && writable && !closed && awaitingIds().length > 0,
      describe: withIds('review_impact', awaitingIds),
      targetOf: (a) => `impact:${a.impactId}`,
      run: async (a) => {
        const r = rowOfImpact(a.impactId);
        if (!reviewable(r) || !r) return `The impact ${String(a.impactId)} is not awaiting its review.`;
        const comment = String(a.comment ?? '').trim();
        if (!comment) return 'A review comment is mandatory.';
        return (await reviewRow(r, a.outcome === 'accept', comment)) ? undefined : error || 'The review failed.';
      },
    },
    {
      name: 'rename_change',
      enabled: () => !!change && !closed && !defining,
      run: async (a) => {
        const t = String(a.title ?? '').trim();
        if (!t) return 'The title cannot be empty.';
        return (await define({ title: t }, 'Change updated.')) ? undefined : error || 'The change could not be renamed.';
      },
    },
    {
      name: 'update_intent',
      enabled: () => !!change && !closed && !defining,
      run: async (a) => {
        const t = String(a.intent ?? '').trim();
        if (!t) return 'The intent cannot be empty.';
        return (await define({ intent: t }, 'Change updated.')) ? undefined : error || 'The intent could not be updated.';
      },
    },
    {
      name: 'move_change',
      enabled: () => !!change && !closed && movable(change),
      run: async (a) => {
        if (!change?.id) return 'No change is open.';
        const offer = await loadMoveOffer(change);
        if (offer.blocked) return offer.blocked;
        if (!offer.choices.some((c) => c.key === a.project)) return `The project ${String(a.project)} is not offered: ${offer.choices.map((c) => c.key).join(', ')}.`;
        await moveChangeTo(change.id, String(a.project));
        recordAction('moved the change to another project');
        moveOpen = false;
        await load(change.id);
      },
    },
  ];

  $effect(() =>
    registerAssist({
      tab: tab.id,
      screen: () =>
        change
          ? {
              kind: 'change',
              title: change.title || 'Untitled change',
              summary: changeSummary(
                { title: change.title, status: change.status, lifecycle: change.lifecycle, state: change.state, project: change.projectId, methodology: change.methodology, namespace: change.namespace, parentId: change.parentId },
                impactFacts,
                { pane, scope, filter: impactFilter },
              ),
              entities: [
                ...transitionEntities(
                  offers.map((o) => ({ name: o.name, to: o.to, needsDecision: o.needsDecision })),
                  pickable.map((p) => ({ id: p.id ?? '', label: decisionLabel(p) })),
                ),
                ...stepEntities(startPoints?.points ?? []),
                ...impactEntities(impactFacts),
              ],
            }
          : undefined,
      focus: () => {
        const sel = impactFacts.find((i) => i.id === selectedImpact);
        return {
          element: sel ? { type: 'impact', id: sel.id, label: sel.key } : undefined,
          dialogKind: compareOpen ? 'compare' : defining ? 'edit' : moveOpen ? 'move' : undefined,
          dialogTitle: compareOpen ? 'Compare and decide options' : defining ? 'Edit the title and intent of the change' : moveOpen ? 'Move the change to another project' : undefined,
          pendingAction: defining ? 'editing the title and intent of the change' : moveOpen ? 'choosing the project to move the change to' : applying ? 'applying the change' : undefined,
          errors: [error, checkError].filter(Boolean),
        };
      },
      tools: assistTools,
    }),
  );

  provideActions(
    () => tab.id,
    () => [
      { id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: () => load(selected) },
      {
        id: 'audit',
        label: 'Audit',
        icon: 'list',
        disabled: !change,
        title: 'Ticks, actions, model calls, decisions and impacts of this change',
        run: () => openAudit(),
      },
      {
        id: 'apply',
        label: applying ? 'Applying…' : 'Apply',
        icon: 'check',
        primary: true,
        disabled: !change || isApplied || applying || unnamed || stuckNotLandable,
        title: stuckNotLandable ? 'Move the nodes to a landable state first' : isSub ? 'Integrate the change into its parent change' : 'Create a new baseline from the change',
        run: apply,
      },
    ],
  );
</script>


{#snippet scopeHead(what: string, count: number)}
  <div class="scope-head"><strong>{what}</strong> <span class="count">{count}</span></div>
{/snippet}

{#snippet producer(i: ChangeItem)}
  {#if i.execution}
    <button type="button" class="link" title={provenance(i)} onclick={() => openAudit(i.execution)}>{i.producedBy || shortId(i.execution)}</button>
  {:else}
    <span title={provenance(i)}>{i.producedBy}</span>
  {/if}
  {#if i.supersedes?.length}<span class="hint" title={provenance(i)}> (supersedes {i.supersedes.length})</span>{/if}
{/snippet}

<div class="editor-page">
{#if missing}<NotFound {tab} what="Change" />{:else if error}<div class="alert">{error}</div>{/if}

{#if loading && !change}
  <p class="empty">Loading…</p>
{/if}

{#if change}
  {@const ch = change}
  <ScopeBar changeId={ch.id ?? ''} {options} bind:scope {candidates} {mainImpacts} {closed} {writable} onchange={() => load(selected)} oncompare={() => (compareOpen = true)} />
  {#if ch.lifecycle}
    <ChangeLifecycleView lifecycle={ch.lifecycle} current={ch.state ?? ''} definition={lcDef} domain={lcDomain} loading={lcLoading} failure={lcFailure}>
      {#if lcDef && lifecycleMovable(ch)}
        <ChangeTransitions current={ch.state ?? ''} {offers} {pickable} busy={transitioning} refusal={transitionRefusal} onmove={(o, d) => void takeTransition(o, d)} />
      {/if}
    </ChangeLifecycleView>
  {/if}
  {#if compareOpen && options.length}
    <CompareDialog
      changeId={ch.id ?? ''}
      {options}
      {scope}
      {closed}
      onchange={() => load(selected)}
      onclose={() => (compareOpen = false)}
      onshow={(s, _d) => ((scope = s), (pane = 'impacts'), (compareOpen = false))}
      onopennode={(s, d) => openNode({ id: d.node ?? '', key: d.key ?? '' }, { pin: true, change: ch.id ?? '', flow: s })}
    />
  {/if}
  <EditorPanes {panes} bind:active={pane} label="Change sections">
    {#snippet children(active)}
      {#if active === 'overview'}
      <section class="card">
        <div class="editor-head">
          <Icon name="diff" size={18} />
          <h2>{ch.title || 'Untitled'}</h2>
          <StatusBadge status={ch.status} />
          <ChangeCriticality change={ch} {closed} onchange={() => load(selected)} />
          <span class="grow"></span>
          {#if !closed && !defining}
            <button type="button" class="small" onclick={startDefine}>Edit</button>
            {#if movable(ch)}<button type="button" class="small" onclick={() => (moveOpen = !moveOpen)}>Move to project…</button>{/if}
            <button type="button" class="small danger" disabled={defBusy} onclick={abandon}>Abandon</button>
          {/if}
        </div>
        {#if moveOpen && movable(ch)}
          <MoveChange change={ch} oncancel={() => (moveOpen = false)} onmoved={() => ((moveOpen = false), load(selected))} />
        {/if}
        {#if defining}
          <form class="define" onsubmit={(e) => (e.preventDefault(), define({ title: defTitle.trim(), intent: defIntent.trim() }, 'Change updated.'))}>
            <label for="def-title">Title</label>
            <input id="def-title" type="text" bind:value={defTitle} />
            <label for="def-intent">Intent</label>
            <textarea id="def-intent" rows="2" bind:value={defIntent}></textarea>
            <div class="row">
              <button type="submit" class="primary small" disabled={defBusy || !defTitle.trim()}>Save</button>
              <button type="button" class="small" onclick={() => (defining = false)}>Cancel</button>
            </div>
          </form>
        {:else if ch.intent}<p class="intent">"{ch.intent}"</p>{/if}
        <dl class="meta">
          <dt>ID</dt><dd><code>{ch.id}</code></dd>
          {#if ch.namespace}<dt>Namespace</dt><dd>{ch.namespace}</dd>{/if}
          {#if ch.projectId}<dt>Project</dt><dd><code>{ch.projectId}</code></dd>{/if}
          {#if ch.ownerOrg}<dt>Owner unit</dt><dd><code>{ch.ownerOrg}</code></dd>{/if}
          {#if ch.parentId}
            <dt>Parent change</dt>
            <dd><button type="button" class="link mono" onclick={() => openTab({ kind: 'change', params: { id: ch.parentId ?? '' } })}>{shortId(ch.parentId)}</button></dd>
          {/if}
          {#if ch.methodology}<dt>Methodology</dt><dd>{ch.methodology}</dd>{/if}
          {#if ch.goal}
            <dt>Goal</dt>
            <dd>
              <code>{ch.goal}</code>
              {#if goalNote.state === 'unknown'}<span class="hint"> unknown goal: the methodology {ch.methodology} no longer declares it</span>
              {:else if goalNote.description}<span class="hint"> {goalNote.description}</span>{/if}
            </dd>
          {/if}
          {#if ch.baselineId}
            <dt>Starting baseline</dt>
            <dd><button type="button" class="link mono" onclick={() => openBaseline(ch.baselineId)}>{shortId(ch.baselineId)}</button></dd>
          {/if}
          {#if ch.resultBaselineId}
            <dt>Resulting baseline</dt>
            <dd><button type="button" class="link mono" onclick={() => openBaseline(ch.resultBaselineId)}>{shortId(ch.resultBaselineId)}</button></dd>
          {/if}
          {#if ch.createdAt}<dt>Created on</dt><dd>{formatDate(ch.createdAt)}</dd>{/if}
          <dt>Audit</dt>
          <dd><button type="button" class="link" onclick={() => openAudit()}>Ticks, actions, model calls, decisions and impacts</button></dd>
          {#if related.length}
            <dt>Executions</dt>
            <dd class="runs">
              {#each related as p (p.id)}
                <button type="button" class="link" onclick={() => openTab({ kind: 'run', params: { id: p.id ?? '' } })}>{p.agent || shortId(p.id)}</button>
                <StatusBadge status={p.status} />
              {/each}
            </dd>
          {/if}
        </dl>
        {#each related.filter((p) => !p.parentId) as p (p.id)}
          <ProcessProgress processId={p.id ?? ''} onopen={(id) => openTab({ kind: 'run', params: { id } })} />
        {/each}
        {#if showSteps}
          <PossibleSteps
            points={startPoints}
            error={pointsError}
            busy={startingId}
            onstart={(p) => void startPoint(p)}
            choices={methodologyChoices}
            chosen={pointsMethodology}
            onchoose={choosePointsMethodology}
          />
        {/if}

        {#if ch.status === 'committed'}
          <div class="alert warn" style="margin: 0.75rem 0">
            <p>Committed on its own branch; the integration into {mergeInto || 'the parent branch'} waits: resolve the nodes changed on both sides.</p>
            {#if mergeInto}
              <MergeResolver namespace={namespaceOf(ch.namespace)} from={ch.branch ?? ''} into={mergeInto} onmerge={merge} />
            {/if}
          </div>
        {/if}

        <div class="board-check">
          <button onclick={checkBoard} disabled={checking}>{checking ? 'Checking…' : 'Blackboard check'}</button>
          {#if checkError}<span class="error">{checkError}</span>{/if}
          {#if boardIssues && !boardIssues.length}<span class="hint">Consistent</span>{/if}
          {#if boardIssues?.length}
            {@const nWarn = boardIssues.filter((i) => i.severity === 'warning').length}
            {@const nErr = boardIssues.length - nWarn}
            <p class="hint">
              {nErr} error{nErr === 1 ? '' : 's'}, {nWarn} warning{nWarn === 1 ? '' : 's'} on {scopeLabel}
            </p>
            <BoardIssueList issues={boardIssues} sections />
          {/if}
        </div>

        {#if relaunches.length}
          <h3>Flow branches <span class="count">{relaunches.length}</span></h3>
          <FlowGraph {processes} changeId={selected} flows={relaunches} onopen={(pid) => openTab({ kind: 'run', params: { id: pid } })} />
          <ul class="subs flows">
            {#each relaunches as f (f.id)}
              {@const fp = flowProcess(f)}
              <li>
                <StatusBadge status={flowBadge(f)} />
                <code>{shortId(f.id)}</code>
                from step {flowStepNumber(f, processes)}
                {#if f.origin?.reason}<span class="muted">· {f.origin.reason}</span>{/if}
                <span class="hint">· {f.stale?.length ?? 0} stale item(s)</span>
                {#if f.origin?.process}<button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: f.origin?.process ?? '' } })}>previous run</button>{/if}
                {#if fp}<button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: fp.id ?? '' } })}>relaunched run</button>{/if}
                <div class="flow-row"><FlowBranchInfo flow={f} namespace={namespaceOf(change?.namespace)} changeBranch={change?.branch ?? ''} /></div>
                <div class="flow-row"><FlowActions flow={f} changeId={selected} ondecided={() => load(selected)} /></div>
              </li>
            {/each}
          </ul>
        {/if}

        <SubChangeSync change={ch} {closed} onchange={() => load(selected)} />
        <div class="apply row">
          {#if isSub}
            <p class="grow hint">Applying integrates this sub-change into its parent change, which lands it: no baseline of its own.</p>
          {:else}
            <div class="grow">
              <label for="bname">Name of the new baseline</label>
              <input id="bname" type="text" bind:value={baselineName} disabled={isApplied} />
            </div>
          {/if}
          <button class="primary" onclick={apply} disabled={isApplied || applying || unnamed || stuckNotLandable || awaiting.length > 0} title={stuckNotLandable ? 'Move the nodes to a landable state first' : awaiting.length ? 'Review the impacts first' : ''}>
            {applying ? 'Applying…' : isSub ? 'Integrate into parent' : 'Apply'}
          </button>
        </div>
        {#if awaiting.length}
          <div class="alert" role="status" style="margin: 0.75rem 0 0">
            Apply is blocked: {awaiting.length} change impact{awaiting.length > 1 ? 's' : ''} await{awaiting.length > 1 ? '' : 's'} their review ({awaiting.map((n) => n.key).join(', ')}).
            <button type="button" class="link" onclick={() => (pane = 'impacts')}>Review them</button>
          </div>
        {/if}
        {#if applied}
          <div class="alert ok" style="margin: 0.75rem 0 0">
            Baseline <button type="button" class="link" onclick={() => openBaseline(applied?.id)}>{applied.name || applied.id}</button> created.
          </div>
        {/if}
      </section>
      {:else if active === 'decisions'}
      <section class="card">
        <h3>Decision points</h3>
        <ChangeDecisions changeId={ch.id ?? ''} {closed} onchange={() => load(selected)} />
      </section>
      {:else if active === 'reviews'}
        <div class="scoped" style="--scope: {scopeTint}">
          <ReviewPanel change={ch} nodes={(view?.nodes ?? []).filter((n) => !n.superseded)} scope={scope || MAIN_SCOPE} closed={!writable} onopenimpact={() => (pane = 'impacts')} />
        </div>
      {:else if active === 'risks'}
        <ChangeRisks change={ch} {closed} onchange={() => load(selected)} />
      {:else if active === 'verification'}
        <ChangeVerification change={ch} />
      {:else if active === 'derogations'}
        <ChangeDerogations change={ch} {closed} onchange={() => load(selected)} />
      {:else if active === 'impacts'}
      <div class="scoped" style="--scope: {scopeTint}">
      {@render scopeHead('Change impacts', lcRows.length)}
      <div class="row filter">
        <label for="impact-filter">Show</label>
        <select id="impact-filter" bind:value={impactFilter}>
          <option value="all">all impacts</option>
          <option value="proposed">awaiting review</option>
          <option value="accepted">accepted</option>
          <option value="rejected">rejected</option>
        </select>
      </div>
      <ChangeLifecycle
        impacts
        selected={selectedImpact}
        scope={scope || MAIN_SCOPE}
        rows={shownRows}
        candidates={lcCandidates}
        disabled={!writable}
        busy={moving}
        onmove={move}
        onedit={edit}
        types={typeNames}
        {lifecycleOf}
        keys={takenKeys}
        oncreate={createNode}
        onremove={removeNode}
        onreview={reviewRow}
        onacceptall={acceptAll}
        onhistory={(r) => openNode(r.node, { pin: true, generic: true, pane: 'history' })}
        onopennode={(r) => openNode({ id: r.impact?.post?.id ?? r.node.id ?? '', key: r.node.key ?? '' }, { pin: true, change: ch.id ?? '', flow: scope || MAIN_SCOPE })}
        onadd={addImpact}
      />
      </div>
      {:else if active === 'artifacts'}
      <div class="scoped" style="--scope: {scopeTint}">
      {@render scopeHead('Artifacts', artifacts.length)}
      <section class="card">
        {#each artifacts as i (i.id)}
          {@const md = markdownOf(i)}
          <article class="artifact" class:superseded={i.status === ITEM_SUPERSEDED}>
            <h4>
              {#if i.type === 'guidance'}<span class="badge-guidance">guidance</span>{:else}{i.type || 'artifact'}{/if}
              <span class="hint">· {@render producer(i)}</span>
              {#if i.status === ITEM_SUPERSEDED || i.status === 'candidate' || i.status === 'stale' || i.status === 'rejected'}<StatusBadge status={i.status} />{/if}
            </h4>
            {#if i.type === 'guidance' && typeof i.data?.text === 'string'}
              <blockquote class="guidance-quote">{i.data.text}</blockquote>
            {:else if md !== undefined}
              <pre class="md">{md}</pre>
            {:else if i.data}
              <pre>{JSON.stringify(i.data, null, 2)}</pre>
            {/if}
            {#each decisionsOn.get(i.id ?? '') ?? [] as d (d.id)}
              <p class="item-decision" class:superseded={d.status === ITEM_SUPERSEDED}>
                <StatusBadge status={d.decision?.accept ? 'accepted' : 'rejected'} />
                {#if d.decision?.comment}<span> — {d.decision.comment}</span>{/if}
                {#if d.producedBy || d.createdAt}<span class="hint"> · {d.producedBy ?? ''}{d.producedBy && d.createdAt ? ' ' : ''}{d.createdAt ? new Date(d.createdAt).toLocaleString() : ''}</span>{/if}
              </p>
            {/each}
          </article>
        {:else}
          <p class="empty">No artifacts.</p>
        {/each}
      </section>

      {#if raw.length}
        <details class="card">
          <summary>Raw items (debug) <span class="count">{raw.length}</span></summary>
          <pre>{JSON.stringify(raw, null, 2)}</pre>
        </details>
      {/if}
      </div>
      {:else if active === 'audit'}
      <section class="card">
        <ChangeAudit change={ch} bind:process={auditProcess} bind:run={auditRun} onrun={(pid) => openTab({ kind: 'run', params: { id: pid } })} />
      </section>
      {:else if active === 'changes'}
      <section class="card">
        <h3>Parent changes <span class="count">{ancestors.length}</span></h3>
        {#if ancestors.length}
          <ul class="subs">
            {#each ancestors as a, i (a.id)}
              <li style="padding-left: {i * 1}rem">
                <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: a.id ?? '' } })}>{a.title || shortId(a.id)}</button>
                {#if a.ownerOrg}<code>{a.ownerOrg}</code>{/if}
                <StatusBadge status={a.status} />
              </li>
            {/each}
          </ul>
        {:else}
          <p class="empty">This change has no parent.</p>
        {/if}
      </section>

      <section class="card">
          <h3>Sub-changes <span class="count">{subs.length}</span></h3>
          {#if subs.length}
            <ul class="subs">
              {#each subs as s (s.id)}
                <li>
                  <button type="button" class="link" onclick={() => openTab({ kind: 'change', params: { id: s.id ?? '' } })}>{s.title || shortId(s.id)}</button>
                  {#if s.ownerOrg}<code>{s.ownerOrg}</code>{/if}
                  <StatusBadge status={s.status} />
                </li>
              {/each}
            </ul>
          {:else}
            <p class="empty">No sub-changes.</p>
          {/if}
          {#if ownBranch && !closed}
            <button type="button" onclick={split} disabled={splitting} title="One sub-change per unit owning the impacted nodes">
              {splitting ? 'Splitting…' : 'Split by owner'}
            </button>
          {/if}
          {#if openSubs.length}<p class="hint">Apply or abandon the {openSubs.length} open sub-change(s) before applying this change.</p>{/if}
      </section>

      {#if ancestors.length || subs.length}
        <section class="card">
          <h3>History with {ancestors.length ? 'the parent' : ''}{ancestors.length && subs.length ? ' and ' : ''}{subs.length ? 'the sub-changes' : ''}</h3>
          <SubChangeHistory change={ch} parent={ancestors.at(-1)} {subs} />
        </section>
      {/if}
      {/if}
    {/snippet}
  </EditorPanes>
{/if}
</div>

<style>
  .filter {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    margin: 0.3rem 0;
  }
  .item-decision {
    margin: 0.4rem 0 0;
    font-size: 0.85em;
  }
  .scoped {
    border-left: 3px solid var(--scope);
    padding-left: 8px;
  }
  .scope-head {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    margin: 4px 0 8px;
  }
  .subs {
    list-style: none;
    margin: 0 0 0.5rem;
    padding: 0;
  }
  .subs li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.15rem 0;
  }
  .hint {
    color: var(--text-muted, inherit);
    font-size: 0.88em;
  }
  .runs {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem 0.5rem;
    align-items: center;
  }
  .intent {
    margin: 0.6rem 0;
    font-style: italic;
  }
  .define {
    display: flex;
    flex-direction: column;
    gap: 3px;
    margin: 0.6rem 0;
  }
  .define label {
    font-size: 0.8rem;
    color: var(--muted);
  }
  .meta {
    margin: 0.5rem 0 0.8rem;
  }
  .apply {
    align-items: flex-end;
    border-top: 1px solid var(--border);
    padding-top: 0.85rem;
  }
  .count {
    font-size: 0.8rem;
    color: var(--muted);
    font-weight: 500;
    margin-left: 0.3rem;
  }
  .muted {
    color: var(--muted);
  }
  .flows > li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .flows .flow-row {
    flex-basis: 100%;
  }
  .badge-guidance {
    border: 1px solid var(--accent);
    color: var(--accent);
    border-radius: 999px;
    padding: 0 8px;
    font-size: 0.85em;
  }
  .guidance-quote {
    margin: 4px 0;
    padding: 4px 10px;
    border-left: 3px solid var(--accent);
    font-style: italic;
    white-space: pre-wrap;
  }
  .artifact + .artifact {
    margin-top: 1rem;
  }
  .superseded {
    opacity: 0.55;
  }
  .superseded td,
  .superseded h4 {
    text-decoration: line-through;
  }
  .md {
    font-size: 0.88rem;
    line-height: 1.55;
  }
</style>
