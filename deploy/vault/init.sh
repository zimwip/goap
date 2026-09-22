#!/bin/sh
# Seeds the dev Vault (KV v2 at secret/) with the platform secrets.
set -e
export VAULT_ADDR=${VAULT_ADDR:-http://vault:8200}
until vault status >/dev/null 2>&1; do sleep 1; done
vault kv put secret/goap/gateway jwt_secret="${GOAP_JWT_SECRET:-dev-secret-change-me-dev-secret-change-me}"
if [ -n "$ANTHROPIC_API_KEY" ]; then
  vault kv put secret/goap/modelgw anthropic_api_key="$ANTHROPIC_API_KEY"
else
  vault kv put secret/goap/modelgw placeholder=true
fi
echo "vault seeded"
