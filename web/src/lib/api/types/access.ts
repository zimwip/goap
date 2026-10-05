// Access types (proto3 JSON).

// --- access -----------------------------------------------------------------

/** ABAC rule: `rule` is an expression over r.sub, r.obj and r.act. */
export interface Policy {
  rule?: string;
  /** resource type or "*" */
  resource?: string;
  /** action or "*" */
  action?: string;
  effect?: 'allow' | 'deny' | string;
}
