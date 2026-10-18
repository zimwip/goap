// Runner of the claude-coding-agent connector (ADR 0123): one turn of a Claude Code session through the Claude
// Agent SDK. It reads one JSON request on its standard input and writes JSON lines on its output: {"type":"tool"}
// for each tool the session uses, then one {"type":"result"}. It is the only code that knows the SDK; the
// connector (Go) knows this protocol. A session never waits for a person: a question (AskUserQuestion) or a tool the
// permissions do not settle ends the turn, and the connector resumes the session with the person's decision.

const sdk = process.env.GOAP_CLAUDE_AGENT_SDK_MODULE || '@anthropic-ai/claude-agent-sdk';

const emit = (m) => process.stdout.write(JSON.stringify(m) + '\n');

async function readStdin() {
  const chunks = [];
  for await (const c of process.stdin) chunks.push(c);
  return Buffer.concat(chunks).toString('utf8');
}

const instructions = [
  'You are run by GOAP, a platform that drives the change this workspace belongs to; nobody watches this session.',
  'When you need a decision from a person, ask it with AskUserQuestion: the session stops and is resumed with the answers.',
  'End with a short summary of what you did, what you checked and what is left.',
].join('\n');

function statusOf(pending, result) {
  if (pending?.questions) return 'needs_input';
  if (pending?.permission) return 'needs_approval';
  if (!result) return 'failed';
  if (result.subtype === 'success' && !result.is_error) return 'done';
  if (result.subtype === 'error_max_turns' || result.subtype === 'error_max_budget_usd') return 'budget';
  return 'failed';
}

async function main() {
  const req = JSON.parse(await readStdin());
  const { query } = await import(sdk);
  const abort = new AbortController();
  for (const sig of ['SIGTERM', 'SIGINT']) process.on(sig, () => abort.abort());

  let pending = null;
  const canUseTool = async (toolName, input) => {
    if (toolName === 'AskUserQuestion') {
      pending ??= { questions: input?.questions ?? [] };
      return { behavior: 'deny', message: 'A person answers these questions: the session is resumed with the answers.', interrupt: true };
    }
    pending ??= { permission: { tool: toolName, input } };
    return { behavior: 'deny', message: 'This tool needs the approval of a person: the session is resumed once they decided.', interrupt: true };
  };

  const options = {
    cwd: req.cwd,
    abortController: abort,
    canUseTool,
    env: process.env,
    permissionMode: req.permissionMode || 'acceptEdits',
    settingSources: req.settingSources ?? ['project'],
    systemPrompt: { type: 'preset', preset: 'claude_code', append: instructions },
  };
  if (req.resume) options.resume = req.resume;
  if (req.model) options.model = req.model;
  if (req.maxTurns) options.maxTurns = req.maxTurns;
  if (req.maxBudgetUsd) options.maxBudgetUsd = req.maxBudgetUsd;
  if (req.allowedTools?.length) options.allowedTools = req.allowedTools;
  if (req.disallowedTools?.length) options.disallowedTools = req.disallowedTools;
  if (req.plugins?.length) options.plugins = req.plugins.map((path) => ({ type: 'local', path }));

  let sessionId = req.resume || '';
  let result = null;
  const tools = {};
  try {
    for await (const m of query({ prompt: req.prompt, options })) {
      if (m.session_id) sessionId = m.session_id;
      if (m.type === 'assistant') {
        for (const b of m.message?.content ?? []) {
          if (b.type !== 'tool_use') continue;
          tools[b.name] = (tools[b.name] ?? 0) + 1;
          emit({ type: 'tool', name: b.name });
        }
      } else if (m.type === 'result') {
        result = m;
      }
    }
  } catch (e) {
    if (!pending && !result) {
      emit({ type: 'result', status: abort.signal.aborted ? 'cancelled' : 'failed', sessionId, tools, errors: [String(e?.message ?? e)] });
      return;
    }
  }
  emit({
    type: 'result',
    status: statusOf(pending, result),
    sessionId,
    subtype: result?.subtype ?? '',
    summary: result?.result ?? '',
    questions: pending?.questions,
    permission: pending?.permission,
    turns: result?.num_turns ?? 0,
    costUsd: result?.total_cost_usd ?? 0,
    usage: result?.usage,
    tools,
    errors: result?.errors,
  });
}

main().catch((e) => {
  emit({ type: 'result', status: 'failed', errors: [String(e?.message ?? e)] });
  process.exitCode = 1;
});
