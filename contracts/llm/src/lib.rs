//! The provider-agnostic completion contract used by the engine and
//! implemented by the model gateway.

mod llm;
mod meta;
mod observe;
mod serde_util;

pub use llm::*;
pub use meta::*;
pub use observe::*;
