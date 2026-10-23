//! The contracts of proto/ (ADR 0125): the `goap.<package>.v1` messages with serde in the protobuf JSON form, and per
//! service its methods, server trait, router and client over `goap_connect`. Generated at build time.
#![allow(clippy::all, missing_docs)]

/// The encoded `FileDescriptorSet` of proto/ (the proxy decodes any message with it).
pub const DESCRIPTOR: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/descriptor.bin"));

include!(concat!(env!("OUT_DIR"), "/packages.rs"));
