//! Serde helpers of the JSON shapes of the package.

use serde::{Deserialize, Deserializer};

/// Reads a JSON null as the default value (Go's nil slice or map).
pub(crate) fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// `omitempty` of a bool.
pub(crate) fn is_false(b: &bool) -> bool {
    !*b
}
