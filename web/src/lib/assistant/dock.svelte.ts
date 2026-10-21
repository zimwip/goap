// Where the assistant's conversation docks: the lower part of the context bar, on its Assistant tool. None: the
// conversation floats over the corner, by its bubble.
export const dock = $state({ el: undefined as HTMLElement | undefined });

/** Moves the node into the dock while one is given (a Svelte action), and takes it out when it goes. */
export function portal(node: HTMLElement, target: HTMLElement | undefined) {
  const place = (t: HTMLElement | undefined) => t?.appendChild(node);
  place(target);
  return {
    update: place,
    destroy: () => node.remove(),
  };
}

/** Offers an element as the dock (a Svelte action): the last one offered wins, and it leaves when it goes. */
export function offerDock(node: HTMLElement) {
  dock.el = node;
  return {
    destroy: () => {
      if (dock.el === node) dock.el = undefined;
    },
  };
}
