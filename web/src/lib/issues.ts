// Severity of the issues of a methodology (ADR 0097): an error blocks, a warning is a remark that never does.
import type { Issue } from './api';

export const isWarning = (i: Issue): boolean => i.severity === 'warning';

/** The blocking issues and the remarks, in their order. */
export function splitIssues<T extends Issue>(issues: T[]): { errors: T[]; warnings: T[] } {
  return { errors: issues.filter((i) => !isWarning(i)), warnings: issues.filter(isWarning) };
}

/** "2 errors, 1 warning", "no issues"; the empty parts are left out. */
export function issueSummary(issues: Issue[]): string {
  const { errors, warnings } = splitIssues(issues);
  const parts: string[] = [];
  if (errors.length) parts.push(`${errors.length} error${errors.length === 1 ? '' : 's'}`);
  if (warnings.length) parts.push(`${warnings.length} warning${warnings.length === 1 ? '' : 's'}`);
  return parts.length ? parts.join(', ') : 'no issues';
}

/** The counter of the Issues tab: the errors, then the warnings muted by a "w" ("2", "2 · 1w", "1w"); undefined: none. */
export function issueBadge(issues: Issue[]): string | number | undefined {
  const { errors, warnings } = splitIssues(issues);
  if (!errors.length && !warnings.length) return undefined;
  if (!warnings.length) return errors.length;
  return errors.length ? `${errors.length} · ${warnings.length}w` : `${warnings.length}w`;
}
