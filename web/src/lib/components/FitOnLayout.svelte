<script lang="ts">
  // Inside a SvelteFlow: fits the view each time the flow is laid out again (a new level, another display, the first
  // drawing), once the nodes are measured, so that the flow is seen whole wherever it is drawn.
  import { useSvelteFlow } from '@xyflow/svelte';

  let { layout }: { layout: number } = $props();
  const flow = useSvelteFlow();
  $effect(() => {
    void layout;
    const t = setTimeout(() => void flow.fitView({ padding: 0.15, maxZoom: 1 }), 60);
    return () => clearTimeout(t);
  });
</script>
