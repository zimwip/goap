//! The client of the domain registry, with its TTL cache.

mod client;

pub use client::*;

#[cfg(test)]
mod tests;
