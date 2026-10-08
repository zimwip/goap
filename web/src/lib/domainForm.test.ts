import { describe, expect, it } from 'vitest';
import type { Domain } from './api';
import { emptyDomainForm, fromDomainForm, toDomainForm } from './domainForm';

// the change object types of a domain (ADR 0098) have no editor yet: editing and saving the domain keeps them as they are
describe('domainForm', () => {
  const domain: Domain = {
    name: 'risks',
    version: '1',
    nodeTypes: [{ name: 'Requirement' }],
    changeObjectTypes: [
      { name: 'Risk', key: { kind: 'sequence', prefix: 'RISK' }, editor: 'risk-register', attributes: [{ name: 'title', asName: true }] },
      { name: 'Mitigation', key: { kind: 'ref', ref: 'Risk' }, scope: 'workspace' },
    ],
  };

  it('keeps the change object types through the form', () => {
    const form = toDomainForm(domain);
    form.description = 'edited';
    const back = fromDomainForm(form);
    expect(back.changeObjectTypes).toEqual(domain.changeObjectTypes);
    expect(back.description).toBe('edited');
  });

  it('does not share them with the domain it was built from', () => {
    const form = toDomainForm(domain);
    form.changeObjectTypes[0].name = 'Changed';
    expect(domain.changeObjectTypes?.[0].name).toBe('Risk');
  });

  it('sends none for a domain that has none', () => {
    expect(emptyDomainForm().changeObjectTypes).toEqual([]);
    expect(fromDomainForm({ ...emptyDomainForm(), name: 'x' }).changeObjectTypes).toBeUndefined();
  });
});
