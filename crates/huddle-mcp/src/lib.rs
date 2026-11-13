//! The MCP tool library: one registry of typed tools that every transport serves.

mod machine;
pub mod router_log;
mod server;
mod sources;
mod tools;

pub use machine::{Machine, ProviderSetup};
pub use server::{HuddleServer, SourcesFactory, serve_stdio};
pub use sources::{DiskSources, RouterSources, Sources, pick};
pub use tools::version::VersionInfo;
