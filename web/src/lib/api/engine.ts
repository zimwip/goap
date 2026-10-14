import { rpc } from './transport';
import type { Empty } from './types/common';
import type { AssessmentState, AttachChangeRequest, ChangeStateResponse, ConditionExplanation, ItemInput, ListProcessesRequest, Process, ProcessLogEntry, ProcessProgress, Resolution, StartProcessRequest, StartingPointsResponse, TriggerState } from './types/engine';

const ENGINE = 'goap.engine.v1.EngineService';

export const ENGINE_SERVICE = ENGINE;

export const engine = {
  startProcess: (req: StartProcessRequest) =>
    rpc<StartProcessRequest, { process?: Process }>(ENGINE, 'StartProcess', req),
  answerIntent: (processId: string, answer: string) =>
    rpc<{ processId: string; answer: string }, { process?: Process }>(ENGINE, 'AnswerIntent', {
      processId,
      answer,
    }),
  submitHumanInput: (processId: string, items: ItemInput[]) =>
    rpc<{ processId: string; items: ItemInput[] }, { process?: Process }>(ENGINE, 'SubmitHumanInput', {
      processId,
      items,
    }),
  /** the values of the form of a pending human task (ADR 0115), checked by the engine against the form */
  submitHumanForm: (processId: string, form: Record<string, unknown>) =>
    rpc<{ processId: string; form: Record<string, unknown> }, { process?: Process }>(ENGINE, 'SubmitHumanInput', { processId, form }),
  approveAction: (processId: string, approve: boolean, comment: string) =>
    rpc<{ processId: string; approve: boolean; comment: string }, { process?: Process }>(ENGINE, 'ApproveAction', {
      processId,
      approve,
      comment,
    }),
  /** Unblocks a run waiting for conditions or stuck (ADR 0036 §3): retry, or abandon (with a reason). A condition is established on the change, never declared (ADR 0109 §6). */
  unblockProcess: (processId: string, decision: 'retry' | 'abandon', reason = '') =>
    rpc<{ processId: string; decision: string; reason: string }, { process?: Process }>(ENGINE, 'UnblockProcess', {
      processId,
      decision,
      reason,
    }),
  /** Stops a run that has not ended (whoever answers for it may): a run in flight is interrupted, its live sub-runs are cancelled with it. */
  cancelProcess: (processId: string, reason = '') =>
    rpc<{ processId: string; reason: string }, { process?: Process }>(ENGINE, 'CancelProcess', { processId, reason }),
  /** Restarts a run from one of its steps on a new flow branch; returns the new process. */
  relaunchStep: (processId: string, step: number, reason: string, guidance = '') =>
    rpc<{ processId: string; step: number; reason: string; guidance: string }, { process?: Process }>(ENGINE, 'RelaunchStep', {
      processId,
      step,
      reason,
      guidance,
    }),
  /** Adopts (previous outputs superseded) or discards a relaunched flow. */
  decideFlow: (processId: string, adopt: boolean, comment: string) =>
    rpc<{ processId: string; adopt: boolean; comment: string }, { process?: Process }>(ENGINE, 'DecideFlow', {
      processId,
      adopt,
      comment,
    }),
  /** Answers a blackboard inconsistency: relaunch the proposed step, or ignore the issues and go on. */
  resolveBoard: (processId: string, relaunch: boolean, comment: string) =>
    rpc<{ processId: string; relaunch: boolean; comment: string }, { process?: Process; relaunched?: Process }>(
      ENGINE,
      'ResolveBoard',
      { processId, relaunch, comment },
    ),
  /** Settles the conflicts of the merge a committed change waits for (by node id), completes it and goes on. */
  resolveMerge: (processId: string, resolutions: Record<string, Resolution>, comment = '') =>
    rpc<{ processId: string; resolutions: Record<string, Resolution>; comment: string }, { process?: Process }>(ENGINE, 'ResolveMerge', {
      processId,
      resolutions,
      comment,
    }),
  /** Continues a run whose apply stopped on the conflicts of a rebase, once they are settled and the impacts reviewed again. */
  resolveRebase: (processId: string, comment = '') =>
    rpc<{ processId: string; comment: string }, { process?: Process }>(ENGINE, 'ResolveRebase', { processId, comment }),
  getProcess: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { process?: Process }>(ENGINE, 'GetProcess', { id }, signal),
  getProcessProgress: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { progress?: ProcessProgress }>(ENGINE, 'GetProcessProgress', { id }, signal),
  /** Where each process of the methodology of a change stands now, whatever the number of runs (ADR 0112). */
  getChangeState: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, ChangeStateResponse>(ENGINE, 'GetChangeState', { changeId }, signal),
  /** How the assessment of each subject of a change reads now: the states its methodology declares, else the defaults (ADR 0116). */
  getAssessmentStates: (changeId: string, signal?: AbortSignal) =>
    rpc<{ changeId: string }, { states?: AssessmentState[] }>(ENGINE, 'GetAssessmentStates', { changeId }, signal),
  /** The steps of the methodology of a change that are possible now towards its goal (ADR 0097). */
  listStartingPoints: (changeId: string, signal?: AbortSignal, methodology = '') =>
    rpc<{ changeId: string; methodology?: string }, StartingPointsResponse>(ENGINE, 'ListStartingPoints', { changeId, ...(methodology ? { methodology } : {}) }, signal),
  /** How a condition of the run's world state got its value against the run's change. */
  explainCondition: (processId: string, condition: string, signal?: AbortSignal) =>
    rpc<{ processId: string; condition: string }, ConditionExplanation>(ENGINE, 'ExplainCondition', { processId, condition }, signal),
  listProcesses: (req: ListProcessesRequest = {}, signal?: AbortSignal) =>
    rpc<ListProcessesRequest, { processes?: Process[] }>(ENGINE, 'ListProcesses', req, signal),
  /** Binds an unbound process (ADR 0031) to an existing change (changeId set) or a new one. */
  attachChange: (req: AttachChangeRequest) => rpc<AttachChangeRequest, { process?: Process }>(ENGINE, 'AttachChange', req),
  /** The process's own log (ADR 0031), independent of whether it has a change. */
  getProcessLog: (processId: string, signal?: AbortSignal) =>
    rpc<{ processId: string }, { entries?: ProcessLogEntry[] }>(ENGINE, 'GetProcessLog', { processId }, signal),
  listTriggers: (signal?: AbortSignal) =>
    rpc<Empty, { triggers?: TriggerState[] }>(ENGINE, 'ListTriggers', {}, signal),
  fireTrigger: (methodology: string, agent: string, trigger: string) =>
    rpc<{ methodology: string; agent: string; trigger: string }, { process?: Process }>(ENGINE, 'FireTrigger', {
      methodology,
      agent,
      trigger,
    }),
};
