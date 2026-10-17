//! The configuration the graph is seeded with at the platform bootstrap.

use goap_ctx::Ctx;
use goap_platform::Secrets;

use crate::error::Error;
use crate::router::{Config, default_config, load_config};

/// The model section of the platform bootstrap: the providers, models and aliases the graph is seeded with when it
/// holds no provider.
pub use goap_graphsvc::ModelsConfig;

/// The configuration the graph is seeded with when it holds no provider: the file (GOAP_MODELS_CONFIG) or, without
/// one, the default (Anthropic when a key is available, the fake provider otherwise).
pub async fn initial_config(ctx: &Ctx, path: &str, secrets: &Secrets) -> Result<Config, Error> {
    if !path.is_empty() {
        return load_config(path);
    }
    let key = secrets
        .get(ctx, "goap/modelgw#anthropic_api_key", "ANTHROPIC_API_KEY")
        .await
        .unwrap_or_default();
    Ok(default_config(!key.is_empty()))
}

/// initial_config as the model section of the platform bootstrap (graphsvc.Boot).
pub async fn boot_config(ctx: &Ctx, path: &str, secrets: &Secrets) -> Result<ModelsConfig, Error> {
    let cfg = initial_config(ctx, path, secrets).await?;
    let (providers, models, aliases) = cfg.objects()?;
    Ok(ModelsConfig {
        providers,
        models,
        aliases,
    })
}
