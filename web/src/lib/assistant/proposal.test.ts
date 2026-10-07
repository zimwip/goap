import { describe, expect, it } from 'vitest';
import { argsSummary, cardView, isCard, runStatus, STATUS_LABEL } from './proposal';
import type { ConversationAction } from '../api';

const agent = (over = {}): ConversationAction =>
  ({
    type: 'start_agent',
    status: 'proposed',
    label: 'Run the review agent on CHG-1',
    rationale: 'You asked for a review.',
    args: { methodology: 'sdlc', agent: 'reviewer', goal: 'reviewed', intent: 'review it', project: 'PROJ-A', changeId: 'CHG-1' },
    ...over,
  }) as ConversationAction;
const write = (over = {}): ConversationAction =>
  ({ type: 'ui_tool', status: 'proposed', level: 'write', tool: 'review_impact', args: { impactId: 'I1', outcome: 'accept', comment: 'checked the test' }, label: 'Accept impact I1', rationale: 'It matches.', project: 'PROJ-A', ...over }) as ConversationAction;

describe('proposal card', () => {
  it('only proposals get a card: start_agent and write screen tools', () => {
    expect(isCard(agent())).toBe(true);
    expect(isCard(write())).toBe(true);
    expect(isCard({ type: 'ui_tool', level: 'effect' } as ConversationAction)).toBe(false);
    expect(isCard({ type: 'open_change' })).toBe(false);
    expect(cardView({ type: 'open_change' })).toBeUndefined();
  });

  it('shows a start_agent proposal: what it launches, on which change, why, with a Launch button', () => {
    const v = cardView(agent())!;
    expect(v).toMatchObject({ kind: 'start_agent', status: 'proposed', statusLabel: STATUS_LABEL.proposed, acceptLabel: 'Launch', canDecide: true, rationale: 'You asked for a review.' });
    expect(v.details).toEqual([
      { label: 'Methodology', value: 'sdlc' },
      { label: 'Agent', value: 'reviewer' },
      { label: 'Goal', value: 'reviewed' },
      { label: 'Intent', value: 'review it' },
      { label: 'Change', value: 'CHG-1' },
      { label: 'Project', value: 'PROJ-A' },
    ]);
  });

  it('shows the new change a start_agent proposal would create', () => {
    const v = cardView(agent({ args: { methodology: 'm', agent: 'a', project: 'P', newChange: { title: 'Impact analysis', intent: 'what breaks' } } }))!;
    expect(v.details.find((d) => d.label === 'New change')?.value).toBe('Impact analysis — what breaks');
  });

  it('follows the statuses of a start_agent: starting, started (run and change), rejected, failed', () => {
    expect(cardView(agent({ status: 'starting' }))).toMatchObject({ status: 'starting', canDecide: false });
    expect(cardView(agent({ status: 'started', result: { processId: 'P1', changeId: 'CHG-9' } }))).toMatchObject({ status: 'started', processId: 'P1', changeId: 'CHG-9', canDecide: false });
    expect(cardView(agent({ status: 'rejected' }))).toMatchObject({ status: 'rejected', canDecide: false });
    expect(cardView(agent({ status: 'failed', error: 'no such agent' }))).toMatchObject({ status: 'failed', error: 'no such agent', canDecide: false });
  });

  it('shows a write proposal with its arguments, its target and an Apply button', () => {
    const v = cardView(write(), { target: 'impact:I1' })!;
    expect(v).toMatchObject({ kind: 'ui_tool', status: 'proposed', acceptLabel: 'Apply', canDecide: true, target: 'impact:I1', title: 'Accept impact I1' });
    expect(v.details.map((d) => d.label)).toEqual(['impactId', 'outcome', 'comment']);
    expect(cardView(write({ target: 'impact:I9' }), { target: 'impact:I1' })!.target).toBe('impact:I9');
  });

  it('does not offer a decision while the web is deciding or applying it', () => {
    expect(cardView(write(), { busy: true })!.canDecide).toBe(false);
    expect(cardView(write({ status: 'accepted' }), { busy: true })).toMatchObject({ status: 'applying', canRetry: false });
  });

  it('shows an accepted write whose outcome was never reported as "accepted, not applied", retryable only when the tool is on screen', () => {
    const accepted = write({ status: 'accepted' });
    expect(cardView(accepted, { registered: true })).toMatchObject({ status: 'unapplied', statusLabel: 'Accepted, not applied', canRetry: true, canDecide: false, retryBlocked: '' });
    const blocked = cardView(accepted, { registered: false })!;
    expect(blocked.canRetry).toBe(false);
    expect(blocked.retryBlocked).toMatch(/not open/);
  });

  it('shows done, failed with the error, and rejected', () => {
    expect(cardView(write({ status: 'done' }))).toMatchObject({ status: 'done', canDecide: false, canRetry: false });
    expect(cardView(write({ status: 'failed', error: 'The impact is not awaiting its review.' }))).toMatchObject({ status: 'failed', error: 'The impact is not awaiting its review.' });
    expect(cardView(write({ status: 'rejected' }))).toMatchObject({ status: 'rejected', canDecide: false });
  });

  it('summarises arguments, clipping long texts and leaving empty ones out', () => {
    const s = argsSummary({ a: 'x'.repeat(300), b: '', c: 3, d: ['x'] });
    expect(s.map((r) => r.label)).toEqual(['a', 'c', 'd']);
    expect(s[0].value.length).toBeLessThanOrEqual(160);
    expect(s[1].value).toBe('3');
  });
});

describe('run status', () => {
  it('names where a run stands', () => {
    expect(runStatus(undefined).label).toBe('Reading the run…');
    expect(runStatus({ status: 'running' })).toEqual({ label: 'Running', tone: 'ok' });
    expect(runStatus({ status: 'waiting', pending: { kind: 'approval' } })).toEqual({ label: 'Waiting for approval', tone: 'warn' });
    expect(runStatus({ status: 'waiting', pending: { kind: 'input' } }).label).toBe('Waiting for input');
    expect(runStatus({ status: 'waiting' }).label).toBe('Waiting');
    expect(runStatus({ status: 'completed' })).toEqual({ label: 'Completed', tone: 'ok' });
    expect(runStatus({ status: 'failed' }).tone).toBe('err');
    expect(runStatus({ status: 'stuck' }).tone).toBe('err');
  });
});
