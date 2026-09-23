<script lang="ts">
  // Dialogue d'identification de l'intention (processus « clarifying »).
  import { engine, errorMessage, type Process } from '../../api';

  let { process, onupdate }: { process: Process; onupdate: (p: Process) => void } = $props();

  let answer = $state('');
  let busy = $state(false);
  let error = $state('');

  const ROLES: Record<string, string> = { user: 'Vous', human: 'Vous', assistant: 'Moteur', system: 'Moteur' };

  async function send(e: SubmitEvent) {
    e.preventDefault();
    if (!answer.trim() || !process.id) return;
    busy = true;
    error = '';
    try {
      const res = await engine.answerIntent(process.id, answer.trim());
      if (res.process) onupdate(res.process);
      answer = '';
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  function pick(text: string) {
    answer = text;
  }
</script>

<section class="card dialogue">
  <h3>Dialogue d'intention</h3>
  {#if process.turns?.length}
    <ol class="turns">
      {#each process.turns as t, i (i)}
        <li class:me={t.role === 'user' || t.role === 'human'}>
          <span class="who">{ROLES[t.role ?? ''] ?? t.role}</span>
          <div class="bubble">{t.text}</div>
        </li>
      {/each}
    </ol>
  {/if}
  {#if process.status === 'clarifying'}
    {#if process.question}<p class="question">{process.question}</p>{/if}
    {#if process.candidates?.length}
      <table class="cands">
        <thead><tr><th>Méthodologie</th><th>Agent</th><th>Objectif</th><th class="num">Confiance</th><th>Raison</th><th></th></tr></thead>
        <tbody>
          {#each process.candidates as c, i (i)}
            <tr>
              <td>{c.methodology ?? ''}</td>
              <td><code>{c.agent ?? ''}</code></td>
              <td><code>{c.goal ?? ''}</code></td>
              <td class="num">
                <span class="bar" style:--w="{Math.round((c.confidence ?? 0) * 100)}%"></span>
                {Math.round((c.confidence ?? 0) * 100)} %
              </td>
              <td class="hint">{c.reason ?? ''}</td>
              <td>
                <button type="button" class="small" onclick={() => pick(c.goal ?? c.agent ?? '')} title="Répondre avec ce choix"
                  >Choisir</button
                >
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <form class="row answer" onsubmit={send}>
      <input class="grow" type="text" bind:value={answer} placeholder="Votre réponse…" aria-label="Réponse" data-no-pin />
      <button class="primary" type="submit" disabled={busy || !answer.trim()}>{busy ? 'Envoi…' : 'Répondre'}</button>
    </form>
    {#if error}<div class="alert" style="margin: 0.5rem 0 0">{error}</div>{/if}
  {/if}
</section>

<style>
  .dialogue {
    border-left: 3px solid var(--info);
  }
  .turns {
    list-style: none;
    margin: 0 0 0.6rem;
    padding: 0;
    display: grid;
    gap: 0.4rem;
  }
  .turns li {
    display: grid;
    gap: 0.1rem;
    justify-items: start;
  }
  .turns li.me {
    justify-items: end;
  }
  .who {
    font-size: 0.78rem;
    color: var(--muted);
  }
  .bubble {
    max-width: 80%;
    padding: 0.35rem 0.65rem;
    border-radius: 10px;
    background: var(--surface-2);
    white-space: pre-wrap;
  }
  .me .bubble {
    background: var(--accent-soft);
  }
  .question {
    font-weight: 600;
  }
  .cands {
    margin-bottom: 0.6rem;
  }
  .bar {
    display: inline-block;
    width: 40px;
    height: 6px;
    border-radius: 3px;
    background: linear-gradient(90deg, var(--accent) var(--w), var(--neutral-soft) var(--w));
    vertical-align: middle;
    margin-right: 0.3rem;
  }
  .answer {
    flex-wrap: nowrap;
  }
</style>
