// Current identity (IamService.WhoAmI), refreshed on every token change.
import { iam, onTokenChange, getToken, type Principal } from '../api';

export const session = $state({
  principal: undefined as Principal | undefined,
  loaded: false,
  error: '',
  hasToken: !!getToken(),
});

export async function refreshIdentity(): Promise<void> {
  session.hasToken = !!getToken();
  try {
    session.principal = (await iam.whoAmI()).principal;
    session.error = '';
  } catch {
    session.principal = undefined;
    session.error = 'unknown identity';
  } finally {
    session.loaded = true;
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
