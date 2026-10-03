// Current identity (GET /api/whoami), refreshed on every token change.
import { whoAmI, onTokenChange, getToken, type Principal } from '../api';

export const session = $state({
  principal: undefined as Principal | undefined,
  loaded: false,
  error: '',
  hasToken: !!getToken(),
});

let latest = 0;

export async function refreshIdentity(): Promise<void> {
  const call = ++latest;
  session.hasToken = !!getToken();
  const done = (principal: Principal | undefined, error: string) => {
    if (call !== latest) return; // a newer token's answer is on its way (or arrived): this one is stale
    session.principal = principal;
    session.error = error;
    session.loaded = true;
  };
  if (!session.hasToken) return done(undefined, '');
  try {
    done(await whoAmI(), '');
  } catch (e) {
    done(undefined, e instanceof Error ? e.message : 'unknown identity');
  }
}

onTokenChange(() => void refreshIdentity());

/** Current subject ('': anonymous or unknown). */
export function me(): string {
  return session.principal?.subject ?? '';
}

export function hasAnyRole(...roles: string[]): boolean {
  return (session.principal?.roles ?? []).some((r) => roles.includes(r));
}
