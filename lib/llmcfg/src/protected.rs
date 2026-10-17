//! The protected aliases of the platform (ADR 0084).

use goap_ctx::Ctx;
use goap_graphapi::{Error, NodeValidator, ValidatedNode, ValidatorQuery};
use serde_json::{Map, Value};

use crate::{Alias, NODE_TYPE_ALIAS, STATE_RETIRED, from_props, go_quote, protected_aliases};

/// ProtectedAliasValidator keeps the protected aliases of the platform (ADR 0084) in place: a change may retarget one but
/// never retire it, rename it or drop its protected flag, and the aliases the platform uses by name are always created
/// protected. Wired onto Graph.Validators (cmd/goap-dev, cmd/graph); the graph knows no alias name.
#[derive(Debug, Clone, Copy, Default)]
pub struct ProtectedAliasValidator;

#[async_trait::async_trait]
impl NodeValidator for ProtectedAliasValidator {
    /// Types runs the validator whenever a change touches an alias.
    fn types(&self) -> Vec<String> {
        vec![NODE_TYPE_ALIAS.to_string()]
    }

    /// Validate checks every alias version the change writes against the one it replaces.
    async fn validate(
        &self,
        _ctx: &Ctx,
        _q: &dyn ValidatorQuery,
        impacted: &[ValidatedNode],
    ) -> Result<(), Error> {
        for v in impacted {
            // a malformed alias is the attribute check's to refuse, not this invariant's
            let Some(post) = alias_of_props(&v.post.properties) else {
                continue;
            };
            let Some(pre) = &v.pre else {
                if is_protected_name(&post.alias) && !post.protected {
                    return Err(Error::invalid(format!(
                        "alias {} is protected: it must be created with protected set: invalid",
                        post.alias
                    )));
                }
                continue;
            };
            let Some(pre) = alias_of_props(&pre.properties) else {
                continue;
            };
            if !pre.protected {
                continue;
            }
            if v.post.state == STATE_RETIRED {
                return Err(Error::invalid(format!(
                    "alias {} is protected: it cannot be retired, only retargeted: invalid",
                    pre.alias
                )));
            }
            if !post.protected {
                return Err(Error::invalid(format!(
                    "alias {} is protected: its protected flag cannot be removed: invalid",
                    pre.alias
                )));
            }
            if post.alias != pre.alias {
                return Err(Error::invalid(format!(
                    "alias {} is protected: it cannot be renamed to {}: invalid",
                    pre.alias,
                    go_quote(&post.alias)
                )));
            }
        }
        Ok(())
    }
}

fn is_protected_name(name: &str) -> bool {
    protected_aliases().contains(&name)
}

/// aliasOfProps reads an alias without checking its target (the protected ones may have none).
fn alias_of_props(props: &Map<String, Value>) -> Option<Alias> {
    from_props(props).ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{ASSISTANT_ALIAS, llmcfg::tests::node};
    use goap_domain::{Link, Node, NodeRef};

    struct NoQuery;

    #[async_trait::async_trait]
    impl ValidatorQuery for NoQuery {
        async fn nodes_of_type(&self, _: &Ctx, _: &str, _: &str) -> Result<Vec<Node>, Error> {
            Ok(vec![])
        }
        async fn out_links_of(&self, _: &Ctx, _: &NodeRef) -> Result<Vec<Link>, Error> {
            Ok(vec![])
        }
        async fn node(&self, _: &Ctx, _: &NodeRef) -> Result<Node, Error> {
            Err(Error::not_found("no node"))
        }
    }

    fn alias(name: &str, protected: bool) -> Node {
        let a = Alias {
            alias: name.into(),
            target: "p/m".into(),
            protected,
        };
        node(&crate::alias_key(name), NODE_TYPE_ALIAS, a.props())
    }

    async fn check(pre: Option<Node>, post: Node) -> Result<(), Error> {
        ProtectedAliasValidator
            .validate(&Ctx::background(), &NoQuery, &[ValidatedNode { post, pre }])
            .await
    }

    #[tokio::test]
    async fn test_protected_alias_validator() {
        let err = check(None, alias(ASSISTANT_ALIAS, false))
            .await
            .unwrap_err();
        assert!(err.is_invalid(), "{err}");
        assert_eq!(
            err.to_string(),
            "alias assistant is protected: it must be created with protected set: invalid"
        );
        check(None, alias(ASSISTANT_ALIAS, true)).await.unwrap();
        check(None, alias("fast", false)).await.unwrap();
        // retargeting is free
        check(
            Some(alias(ASSISTANT_ALIAS, true)),
            alias(ASSISTANT_ALIAS, true),
        )
        .await
        .unwrap();
        let mut retired = alias(ASSISTANT_ALIAS, true);
        retired.state = STATE_RETIRED.into();
        assert!(
            check(Some(alias(ASSISTANT_ALIAS, true)), retired)
                .await
                .is_err()
        );
        assert!(
            check(
                Some(alias(ASSISTANT_ALIAS, true)),
                alias(ASSISTANT_ALIAS, false)
            )
            .await
            .is_err()
        );
        let err = check(Some(alias(ASSISTANT_ALIAS, true)), alias("other", true))
            .await
            .unwrap_err();
        assert_eq!(
            err.to_string(),
            "alias assistant is protected: it cannot be renamed to \"other\": invalid"
        );
        // an unprotected alias moves freely
        let mut r = alias("fast", false);
        r.state = STATE_RETIRED.into();
        check(Some(alias("fast", false)), r).await.unwrap();
    }
}
