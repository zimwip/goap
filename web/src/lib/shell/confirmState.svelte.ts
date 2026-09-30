// App-wide confirmation modal: replaces window.confirm(). One global dialog,
// mounted once at the shell root; requests queue if one is already open.
export interface ConfirmOptions {
  title?: string;
  message: string;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
}

interface ConfirmRequest extends ConfirmOptions {
  resolve: (ok: boolean) => void;
}

export const confirmState = $state<{ current: ConfirmRequest | null }>({ current: null });

const queue: ConfirmRequest[] = [];

export function confirmDialog(options: ConfirmOptions | string): Promise<boolean> {
  const opts = typeof options === 'string' ? { message: options } : options;
  return new Promise<boolean>((resolve) => {
    const request: ConfirmRequest = { ...opts, resolve };
    if (confirmState.current) queue.push(request);
    else confirmState.current = request;
  });
}

export function resolveConfirm(ok: boolean): void {
  const request = confirmState.current;
  if (!request) return;
  confirmState.current = null;
  request.resolve(ok);
  const next = queue.shift();
  if (next) confirmState.current = next;
}
