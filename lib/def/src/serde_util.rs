//! Helpers of the YAML / JSON forms: Go's zero values.

use serde::Deserialize;
use serde::de::Deserializer;

/// Reads a null as the default value (Go decodes null into a nil slice or map, or leaves the zero value).
pub(crate) fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

pub(crate) fn is_false(b: &bool) -> bool {
    !*b
}

pub(crate) fn is_zero_i64(n: &i64) -> bool {
    *n == 0
}
