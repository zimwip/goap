import { rpc } from './transport';
import type { Empty } from './types/common';
import type { AttachChangeRequest, ConditionExplanation, ItemInput, ListProcessesRequest, Process, ProcessLogEntry, ProcessProgress, StartProcessRequest, TriggerState } from './types/engine';

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
  approveAction: (processId: string, approve: boolean, comment: string) =>
    rpc<{ processId: string; approve: boolean; comment: string }, { process?: Process }>(ENGINE, 'ApproveAction', {
      processId,
      approve,
      comment,
    }),
  /** Unblocks a run waiting for conditions or stuck (ADR 0036 §3): waive conditions (with a reason), retry, or abandon (with a reason). */
  unblockProcess: (processId: string, decision: 'waive' | 'retry' | 'abandon', conditions: string[] = [], reason = '') =>
    rpc<{ processId: string; decision: string; conditions: string[]; reason: string }, { process?: Process }>(ENGINE, 'UnblockProcess', {
      processId,
      decision,
      conditions,
      reason,
    }),
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
  getProcess: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { process?: Process }>(ENGINE, 'GetProcess', { id }, signal),
  getProcessProgress: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { progress?: ProcessProgress }>(ENGINE, 'GetProcessProgress', { id }, signal),
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
