import { describe, expect, it } from 'vitest';
import { itemTypeCodeLabel, itemTypePermissionLabel, itemTypeShownIn } from './itemTypes';

describe('item types', () => {
  it('tells where the change tab shows a type', () => {
    expect(itemTypeShownIn('risk')).toBe('a pane of its own in the change tab');
    expect(itemTypeShownIn('assessment')).toBe('a bubble next to the subject it is about');
    expect(itemTypeShownIn('signal')).toBe('the raw list of the Artifacts pane');
  });

  it('reads what the code adds', () => {
    expect(itemTypeCodeLabel({ type: 'risk', code: true })).toMatch(/platform code/);
    expect(itemTypeCodeLabel({ type: 'testResult' })).toMatch(/^none/);
    expect(itemTypeCodeLabel(undefined)).toMatch(/not in force/);
  });

  it('reads the permission', () => {
    expect(itemTypePermissionLabel({ type: 'x' })).toBe('no permission of its own');
    expect(itemTypePermissionLabel({ type: 'derogation', permission: 'derogation:sign', subjectField: 'signatory' })).toBe(
      'the permission derogation:sign, and the writer must be the principal named by data.signatory',
    );
  });
});
