// Arguments of a screen tool (ADR 0092): the same typed subset of JSON Schema the server validates. The server has
// already checked the arguments of a call; the web checks them again before running what it reads from a stored
// message, and checks its own descriptors before sending them (the server refuses a request with an invalid one).
import type { UiArgs, UiParam, UiTool } from '../api/types/assistant';

export const MAX_TOOLS = 30;
export const MAX_NAME = 49;
export const MAX_DESCRIPTION_BYTES = 300;
export const MAX_GUIDANCE_BYTES = 200;
export const MAX_PROPERTIES = 12;
export const MAX_SCHEMA_BYTES = 2 << 10;
export const MAX_ENUM = 30;
export const MAX_ARGS_BYTES = 4 << 10;
export const MAX_STRING_BYTES = 2000;

const enc = new TextEncoder();
const bytes = (s: string): number => enc.encode(s).length;
const NAME = /^[a-z][a-z0-9_.-]{0,48}$/;
const PROP = /^[A-Za-z][A-Za-z0-9_]{0,39}$/;

function checkValue(p: UiParam, v: unknown, where: string, inArray = false): string {
  switch (p.type) {
    case 'string':
      if (typeof v !== 'string') return `${where} must be a string`;
      return bytes(v) > MAX_STRING_BYTES ? `${where} is longer than ${MAX_STRING_BYTES} bytes` : '';
    case 'number':
      return typeof v === 'number' && Number.isFinite(v) ? '' : `${where} must be a number`;
    case 'boolean':
      return typeof v === 'boolean' ? '' : `${where} must be a boolean`;
    case 'enum':
      return typeof v === 'string' && (p.enum ?? []).includes(v) ? '' : `${where} must be one of ${(p.enum ?? []).join(', ')}`;
    case 'array': {
      if (inArray || !Array.isArray(v)) return `${where} must be an array`;
      for (const [i, x] of v.entries()) {
        const e = p.items ? checkValue(p.items, x, `${where}[${i}]`, true) : '';
        if (e) return e;
      }
      return '';
    }
  }
  return `${where} has an unknown type`;
}

/** What is wrong with the arguments of a call to a tool ('' when they fit its schema). */
export function validateArgs(schema: UiArgs | undefined, args: unknown): string {
  if (args === undefined || args === null) args = {};
  if (typeof args !== 'object' || Array.isArray(args)) return 'the arguments must be an object';
  const a = args as Record<string, unknown>;
  if (bytes(JSON.stringify(a)) > MAX_ARGS_BYTES) return `the arguments are longer than ${MAX_ARGS_BYTES} bytes`;
  const props = schema?.properties ?? {};
  for (const k of Object.keys(a)) if (!(k in props)) return `unknown argument ${k}`;
  for (const k of schema?.required ?? []) if (a[k] === undefined || a[k] === null) return `missing argument ${k}`;
  for (const [k, p] of Object.entries(props)) {
    if (a[k] === undefined) continue;
    const e = checkValue(p, a[k], `argument ${k}`);
    if (e) return e;
  }
  return '';
}

function checkParam(p: UiParam, where: string, inArray = false): string {
  if (!['string', 'number', 'boolean', 'enum', 'array'].includes(p.type)) return `${where}: unknown type ${p.type}`;
  if (p.type === 'enum' && (!p.enum || p.enum.length < 1 || p.enum.length > MAX_ENUM)) return `${where}: an enum lists 1 to ${MAX_ENUM} values`;
  if (p.type === 'array') {
    if (inArray || !p.items) return `${where}: an array takes scalar items`;
    if (p.items.type === 'array') return `${where}: arrays are never nested`;
    return checkParam(p.items, `${where}[]`, true);
  }
  return '';
}

/** What is wrong with a descriptor ('' when the server would accept it). */
export function validateDescriptor(t: UiTool): string {
  if (!NAME.test(t.name)) return `tool name ${t.name}`;
  if (!t.description || bytes(t.description) > MAX_DESCRIPTION_BYTES) return `${t.name}: the description is empty or over ${MAX_DESCRIPTION_BYTES} bytes`;
  if (bytes(t.guidance ?? '') > MAX_GUIDANCE_BYTES) return `${t.name}: the guidance is over ${MAX_GUIDANCE_BYTES} bytes`;
  if (t.level !== 'effect' && t.level !== 'write') return `${t.name}: the level is effect or write`;
  const props = Object.entries(t.args?.properties ?? {});
  if (props.length > MAX_PROPERTIES) return `${t.name}: at most ${MAX_PROPERTIES} arguments`;
  for (const [k, p] of props) {
    if (!PROP.test(k)) return `${t.name}: argument name ${k}`;
    const e = checkParam(p, `${t.name}.${k}`);
    if (e) return e;
  }
  for (const k of t.args?.required ?? []) if (!(k in (t.args?.properties ?? {}))) return `${t.name}: required ${k} is not an argument`;
  if (bytes(JSON.stringify(t.args ?? {})) > MAX_SCHEMA_BYTES) return `${t.name}: the schema is over ${MAX_SCHEMA_BYTES} bytes`;
  return '';
}
