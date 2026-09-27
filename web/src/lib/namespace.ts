/** Mirrors domain.DefaultNamespace / domain.MainBranch (pkg/domain/domain.go): every branch/baseline
 * call needs an explicit namespace and branch now that both are namespace-scoped. */
export const DEFAULT_NAMESPACE = 'default';
export const MAIN_BRANCH = 'main';

/** Normalizes a namespace, defaulting an empty one (mirrors domain.NamespaceOf). */
export const namespaceOf = (ns?: string): string => ns || DEFAULT_NAMESPACE;
