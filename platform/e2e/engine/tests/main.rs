//! Port of `platform/e2e/engine` (package enginee2e): the engine run on a real graph. One test binary, one module per
//! Go test file (see Cargo.toml).

mod common;

mod agentcheck;
mod agents;
mod asks;
mod assessmentstates;
mod attach;
mod awaiting;
mod builtins;
mod cancel;
mod changestate;
mod companion;
mod decisions;
mod derogation;
mod enduser;
mod engine;
mod flow;
mod foreach;
mod form;
mod journal;
mod ledger_meta;
mod llmloop;
mod locks;
mod loopbudget;
mod mcp;
mod nodeops;
mod oversight;
mod persistent;
mod reaction;
mod rebase;
mod roles;
mod scoped;
mod sdlc;
mod sdlc_rating;
mod sdlc_workspace;
mod specialization;
mod startbaseline;
mod startingpoints;
mod steps;
mod triggers;
mod verify;
