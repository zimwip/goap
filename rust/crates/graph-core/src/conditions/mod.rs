//! The conditions of a methodology, as the graph instantiates them from what the registry declares (ADR 0163
//! addendum): the definition read back from its nodes ([`stored`]), the conditions it compiles to ([`derive`]) and
//! the variables they read on a blackboard ([`activation`]).
pub mod activation;
pub mod derive;
pub mod stored;

#[cfg(test)]
mod tests;
