<script lang="ts">
  // Home page of the editor area (no tab open).
  import Icon from '../shell/Icon.svelte';
  import { runCommand } from '../shell/commands';
  import { showTool } from '../shell/layout.svelte';

  const SHORTCUTS: [string, string][] = [
    ['Ctrl/Cmd + S', 'Save the active editor'],
    ['Ctrl/Cmd + W (or Alt + W)', 'Close the tab'],
    ['Ctrl/Cmd + J', 'Show / hide the console'],
    ['Ctrl/Cmd + B', 'Show / hide navigation'],
    ['Ctrl/Cmd + P', 'Search for an object (">": commands)'],
    ['Double click', 'Pin a tab or open a pinned item'],
    ['Middle click', 'Close a tab'],
  ];
</script>

<div class="welcome">
  <h1><span class="logo">◆</span> GOAP Workshop</h1>
  <p class="hint">
    Design methodologies (agents, actions, conditions, goals), test intents, and follow runs live.
  </p>
  <div class="cols">
    <section>
      <h2>Get started</h2>
      <ul class="links">
        <li><button type="button" class="link" onclick={() => runCommand('assistant')}><Icon name="chat" size={14} /> Assistant (plain-language request)</button></li>
        <li><button type="button" class="link" onclick={() => runCommand('newMethodology')}><Icon name="plus" size={14} /> New methodology</button></li>
        <li><button type="button" class="link" onclick={() => runCommand('import')}><Icon name="upload" size={14} /> Import a methodology (YAML)</button></li>
        <li><button type="button" class="link" onclick={() => runCommand('test')}><Icon name="flask" size={14} /> New intent test</button></li>
        <li><button type="button" class="link" onclick={() => showTool('left', 'changes')}><Icon name="diff" size={14} /> Changes</button></li>
        <li><button type="button" class="link" onclick={() => showTool('right', 'dsl')}><Icon name="help" size={14} /> Script actions DSL help</button></li>
      </ul>
    </section>
    <section>
      <h2>Shortcuts</h2>
      <table>
        <tbody>
          {#each SHORTCUTS as [k, v] (k)}
            <tr><td><kbd>{k}</kbd></td><td>{v}</td></tr>
          {/each}
        </tbody>
      </table>
    </section>
  </div>
</div>

<style>
  .welcome {
    max-width: 820px;
    margin: 0 auto;
    padding: 3rem 1.5rem;
  }
  h1 {
    font-size: 1.6rem;
  }
  .logo {
    color: var(--accent);
  }
  h2 {
    font-size: 1rem;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 2rem;
    margin-top: 1.5rem;
  }
  .links {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.45rem;
  }
  .links button {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
  }
  kbd {
    font-family: var(--mono);
    font-size: 0.85em;
    border: 1px solid var(--border);
    border-bottom-width: 2px;
    border-radius: 4px;
    padding: 0 4px;
    background: var(--surface);
    white-space: nowrap;
  }
  td {
    border: none;
    padding: 0.15rem 0.5rem 0.15rem 0;
  }
</style>
