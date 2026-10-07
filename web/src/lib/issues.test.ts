import { describe, expect, it } from 'vitest';
import { isWarning, issueBadge, issueSummary, splitIssues } from './issues';

const e = { message: 'e' };
const w = { message: 'w', severity: 'warning' };

describe('issue severity', () => {
  it('treats an empty or unknown severity as an error', () => {
    expect(isWarning(e)).toBe(false);
    expect(isWarning({ severity: 'error' })).toBe(false);
    expect(isWarning(w)).toBe(true);
  });
  it('splits errors and warnings', () => {
    const s = splitIssues([e, w, e]);
    expect(s.errors).toHaveLength(2);
    expect(s.warnings).toEqual([w]);
  });
  it('labels', () => {
    expect(issueSummary([])).toBe('no issues');
    expect(issueSummary([e, w])).toBe('1 error, 1 warning');
    expect(issueSummary([w, w])).toBe('2 warnings');
  });
  it('badges', () => {
    expect(issueBadge([])).toBeUndefined();
    expect(issueBadge([e, e])).toBe(2);
    expect(issueBadge([e, w])).toBe('1 · 1w');
    expect(issueBadge([w])).toBe('1w');
  });
});
