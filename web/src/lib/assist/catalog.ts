// The one catalog of what the assistant is told about the screens (ADR 0092): the descriptor of each screen tool (what
// it does, one line on what is required to feed it, its level and arguments) and the guidance of the fields that are
// not attributes of a node type (an attribute's own tooltip is its guidance). A view registers an implementation by
// tool name (`registerAssist`): a name that is not here is refused, so no tool reaches the model without its guidance
// (`catalog.test.ts` checks every entry against the server's caps). Texts are English, the model's input.
import type { UiArgs, UiTool } from '../api/types/assistant';

export interface ToolDef {
  description: string;
  /** one line (200 bytes): what is required to feed the tool well */
  guidance: string;
  level: 'effect' | 'write';
  args?: UiArgs;
}

const str = (description: string) => ({ type: 'string' as const, description });

export const TOOLS = {
  select_impact: {
    description: 'Show an impact of this change in the Impacts pane and highlight it.',
    guidance: 'impactId: the id of an impact listed in the screen entities (type impact).',
    level: 'effect',
    args: { properties: { impactId: str('the impact id') }, required: ['impactId'] },
  },
  open_impact: {
    description: 'Open the node of an impact of this change in its own editor tab.',
    guidance: 'impactId: the id of an impact listed in the screen entities (type impact).',
    level: 'effect',
    args: { properties: { impactId: str('the impact id') }, required: ['impactId'] },
  },
  filter_impacts: {
    description: 'Filter the impacts of this change by review status.',
    guidance: 'review: all, proposed (awaiting review), accepted or rejected.',
    level: 'effect',
    args: { properties: { review: { type: 'enum', enum: ['all', 'proposed', 'accepted', 'rejected'], description: 'the review status to show' } }, required: ['review'] },
  },
  review_impact: {
    description: 'Accept or reject the change impact that awaits its review.',
    guidance: 'Needs the impact id, accept or reject, and a comment naming what was checked (1-3 sentences). Only for impacts awaiting review.',
    level: 'write',
    args: {
      properties: {
        impactId: str('the impact id'),
        outcome: { type: 'enum', enum: ['accept', 'reject'], description: 'the verdict' },
        comment: str('why: what was checked'),
      },
      required: ['impactId', 'outcome', 'comment'],
    },
  },
  rename_change: {
    description: 'Rename this change.',
    guidance: 'title: a short title saying what the change does.',
    level: 'write',
    args: { properties: { title: str('the new title') }, required: ['title'] },
  },
  update_intent: {
    description: 'Rewrite the intent of this change.',
    guidance: 'intent: the need the change answers (why), one or two sentences.',
    level: 'write',
    args: { properties: { intent: str('the new intent') }, required: ['intent'] },
  },
  move_change: {
    description: 'Move this change to another project that applies its methodology.',
    guidance: 'project: the key of the target project; only open root changes can move.',
    level: 'write',
    args: { properties: { project: str('the project key') }, required: ['project'] },
  },
  transition_change: {
    description: 'Move this change along a transition of its lifecycle, out of its current state.',
    guidance: 'transition: the transition to take out of the current state; use the names offered by the screen. decision: a decided point, when it asks for one.',
    level: 'write',
    args: { properties: { transition: str('the transition name'), decision: str('the decided decision point id, when the transition needs one') }, required: ['transition'] },
  },
  set_field: {
    description: 'Put a value in a field of the form on screen (the person still saves the form).',
    guidance: 'field: an id of the screen entities of type field; value: its value as text, in the type of the field.',
    level: 'write',
    args: { properties: { field: str('the field id'), value: str('the value, as text') }, required: ['field', 'value'] },
  },
  set_title: {
    description: 'Fill the title of the change being created.',
    guidance: 'title: what the change does, a few words.',
    level: 'effect',
    args: { properties: { title: str('the title') }, required: ['title'] },
  },
  set_intent: {
    description: 'Fill the intent of the change being created.',
    guidance: 'intent: why, the need the change answers.',
    level: 'effect',
    args: { properties: { intent: str('the intent') }, required: ['intent'] },
  },
  set_project: {
    description: 'Pick the project of the change being created.',
    guidance: 'project: a project key of the form; it applies the methodologies the change needs.',
    level: 'effect',
    args: { properties: { project: str('the project key') }, required: ['project'] },
  },
  create: {
    description: 'Create the change described by the form on screen.',
    guidance: 'Needs a title (and a project and a namespace); fill the form first.',
    level: 'write',
  },
} satisfies Record<string, ToolDef>;

export type ToolName = keyof typeof TOOLS;

export const isToolName = (n: string): n is ToolName => Object.prototype.hasOwnProperty.call(TOOLS, n);

/** The descriptor of a catalog tool, as `Send` takes it. */
export function descriptor(name: ToolName): UiTool {
  const d: ToolDef = TOOLS[name];
  return { name, description: d.description, guidance: d.guidance, level: d.level, ...(d.args ? { args: structuredClone(d.args) } : {}) };
}

/**
 * Guidance of the fields that are not attributes of a node type, by field id (the part before ':' for a field of one
 * of several rows). An attribute carries its own tooltip.
 */
export const FIELD_GUIDANCE: Record<string, string> = {
  title: 'A short title saying what the change does.',
  intent: 'Why: the need the change answers, one or two sentences.',
  review_comment: 'Why you accept or reject this impact: name what you checked, 1-3 sentences.',
  review_global_comment: 'The general comment of the review, kept on every impact it reviews: what was checked, 1-3 sentences.',
  review_entry_comment: 'Why you accept or reject this impact: name what you checked, 1-3 sentences.',
};

/** What a field with no guidance of its own is told. */
export const FIELD_GUIDANCE_DEFAULT = 'The value of this property, in the type the field expects.';

/** The guidance of a field: its own, the attribute tooltip, the catalog's by id, else the default. Never empty. */
export function fieldGuidance(f: { id: string; guidance?: string; description?: string }): string {
  return f.guidance || f.description || FIELD_GUIDANCE[f.id] || FIELD_GUIDANCE[f.id.split(':')[0]] || FIELD_GUIDANCE_DEFAULT;
}
