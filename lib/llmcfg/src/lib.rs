//! Package llmcfg holds the configuration of the model gateway as graph data (platform namespace):
//! providers, the models offered on the platform with their quota and access level, and the aliases.
//! API keys are never in the graph, only a reference to the secret. Token usage is not configuration and
//! stays in the gateway's own table, keyed by the key of the model node.

mod behavior;
mod llmcfg;
mod protected;

pub use behavior::*;
pub use llmcfg::*;
pub use protected::*;
