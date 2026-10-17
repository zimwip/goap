//! The string types of the package (Go's `type X string`): identifiers and the open enumerations whose constants
//! are values of the type (a stored value may be one the code does not know, which the validations report).

/// Declares a string newtype: a `Cow<'static, str>` so that its constants are `const`, serialized as a JSON string
/// (a JSON null reads as the empty value, as Go's zero value).
macro_rules! string_type {
    ($(#[$m:meta])* $name:ident) => {
        $(#[$m])*
        #[derive(Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
        pub struct $name(::std::borrow::Cow<'static, str>);

        impl $name {
            /// A value of a static text (the constants).
            pub const fn from_static(s: &'static str) -> Self {
                Self(::std::borrow::Cow::Borrowed(s))
            }

            /// A value of any text.
            pub fn new(s: impl Into<String>) -> Self {
                Self(::std::borrow::Cow::Owned(s.into()))
            }

            /// The text of the value.
            pub fn as_str(&self) -> &str {
                &self.0
            }

            /// Reports the empty value (Go's `""`).
            pub fn is_empty(&self) -> bool {
                self.0.is_empty()
            }

            /// The text of the value, owned.
            pub fn into_string(self) -> String {
                self.0.into_owned()
            }
        }

        impl ::std::fmt::Display for $name {
            fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
                f.write_str(&self.0)
            }
        }

        impl ::std::fmt::Debug for $name {
            fn fmt(&self, f: &mut ::std::fmt::Formatter<'_>) -> ::std::fmt::Result {
                ::std::fmt::Debug::fmt(&*self.0, f)
            }
        }

        impl ::std::ops::Deref for $name {
            type Target = str;
            fn deref(&self) -> &str {
                &self.0
            }
        }

        impl AsRef<str> for $name {
            fn as_ref(&self) -> &str {
                &self.0
            }
        }

        impl ::std::borrow::Borrow<str> for $name {
            fn borrow(&self) -> &str {
                &self.0
            }
        }

        impl From<String> for $name {
            fn from(s: String) -> Self {
                Self::new(s)
            }
        }

        impl From<&str> for $name {
            fn from(s: &str) -> Self {
                Self::new(s)
            }
        }

        impl From<&String> for $name {
            fn from(s: &String) -> Self {
                Self::new(s.clone())
            }
        }

        impl From<$name> for String {
            fn from(s: $name) -> String {
                s.into_string()
            }
        }

        impl PartialEq<str> for $name {
            fn eq(&self, other: &str) -> bool {
                &*self.0 == other
            }
        }

        impl PartialEq<&str> for $name {
            fn eq(&self, other: &&str) -> bool {
                &*self.0 == *other
            }
        }

        impl PartialEq<String> for $name {
            fn eq(&self, other: &String) -> bool {
                *self.0 == **other
            }
        }

        impl PartialEq<$name> for str {
            fn eq(&self, other: &$name) -> bool {
                self == &*other.0
            }
        }

        impl PartialEq<$name> for &str {
            fn eq(&self, other: &$name) -> bool {
                *self == &*other.0
            }
        }

        impl ::serde::Serialize for $name {
            fn serialize<S: ::serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
                s.serialize_str(&self.0)
            }
        }

        impl<'de> ::serde::Deserialize<'de> for $name {
            fn deserialize<D: ::serde::Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
                <Option<String> as ::serde::Deserialize>::deserialize(d).map(|s| Self::new(s.unwrap_or_default()))
            }
        }
    };
}

pub(crate) use string_type;
