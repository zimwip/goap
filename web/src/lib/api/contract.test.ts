// The hand-written types of the API (types/*.ts) against the contract: the JSON forms generated from the protos
// (src/gen, `make generate`, ADR 0125). An interface named like a message carries the message's fields and no field
// the message lacks, so the web cannot drift from the services silently.
import { describe, expect, it } from 'vitest';
import ts from 'typescript';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

/** web type → the message it mirrors, when the names differ */
const ALIASES: Record<string, string> = {
  PendingSettings: 'PendingSettingsResponse',
};

/** fields a web type carries that its message lacks, and why */
const EXCEPTIONS: Record<string, string> = {
  'Principal.project': 'the session principal (GET /api/whoami, access.Session) carries the active project',
  'Algorithm.mcp': 'the adapter form reuses the algorithm shape: an AdapterDef node carries its MCP (ADR 0061)',
  'Algorithm.connector': 'the adapter form reuses the algorithm shape: an AdapterDef node carries its connector (ADR 0061)',
  'ImpactEvent.branch': 'also the payload of an impact.* log entry (domain.ImpactEvent), which carries it',
  'ImpactEvent.patch': 'also the payload of an impact.* log entry (domain.ImpactEvent), which carries it',
};

function handTypes(): Map<string, Set<string>> {
  const dir = join(__dirname, 'types');
  const out = new Map<string, Set<string>>();
  for (const f of readdirSync(dir).filter((f) => f.endsWith('.ts'))) {
    const src = ts.createSourceFile(f, readFileSync(join(dir, f), 'utf8'), ts.ScriptTarget.Latest, true);
    src.forEachChild((n) => {
      if (!ts.isInterfaceDeclaration(n) || n.heritageClauses) return;
      const props = new Set<string>();
      for (const m of n.members) if (m.name && (ts.isIdentifier(m.name) || ts.isStringLiteral(m.name))) props.add(m.name.text);
      out.set(n.name.text, props);
    });
  }
  return out;
}

type Desc = { kind?: string; typeName?: string; fields?: { jsonName: string }[] };
const generated = import.meta.glob('../../gen/**/*_pb.ts', { eager: true }) as Record<string, Record<string, Desc>>;

/** message name → the fields of every message of that name (several packages may use one name) */
function messages(): Map<string, { typeName: string; fields: Set<string> }[]> {
  const out = new Map<string, { typeName: string; fields: Set<string> }[]>();
  for (const mod of Object.values(generated)) {
    for (const [k, v] of Object.entries(mod)) {
      if (!k.endsWith('Schema') || v?.kind !== 'message') continue;
      const name = k.slice(0, -'Schema'.length);
      out.set(name, [...(out.get(name) ?? []), { typeName: v.typeName ?? name, fields: new Set((v.fields ?? []).map((f) => f.jsonName)) }]);
    }
  }
  return out;
}

describe('API types and the contract', () => {
  const hand = handTypes();
  const msgs = messages();

  it('finds the generated messages', () => {
    expect(msgs.size).toBeGreaterThan(300);
    expect(hand.size).toBeGreaterThan(100);
  });

  it('every web type carries the fields of its message, and only those', () => {
    const drift: string[] = [];
    let compared = 0;
    for (const [name, props] of hand) {
      const candidates = msgs.get(ALIASES[name] ?? name);
      if (!candidates) continue;
      compared++;
      // a name used by several packages: the closest message
      const best = candidates
        .map((c) => ({
          ...c,
          missing: [...c.fields].filter((f) => !props.has(f)),
          extra: [...props].filter((p) => !c.fields.has(p) && !EXCEPTIONS[`${name}.${p}`]),
        }))
        .sort((a, b) => a.missing.length + a.extra.length - (b.missing.length + b.extra.length))[0];
      if (best.missing.length) drift.push(`${name} lacks ${best.missing.join(', ')} of ${best.typeName}`);
      if (best.extra.length) drift.push(`${name} has ${best.extra.join(', ')}, not in ${best.typeName}`);
    }
    expect(compared).toBeGreaterThan(100);
    expect(drift).toEqual([]);
  });

  it('every exception names a field the message lacks', () => {
    for (const key of Object.keys(EXCEPTIONS)) {
      const [name, field] = key.split('.');
      expect(hand.get(name)?.has(field), key).toBe(true);
      expect((msgs.get(ALIASES[name] ?? name) ?? []).every((c) => !c.fields.has(field)), key).toBe(true);
    }
  });
});
