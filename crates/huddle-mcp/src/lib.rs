//! The MCP tool library: one registry of typed tools that every transport serves.

mod server;
mod sources;
mod tools;

pub use server::{HuddleServer, SourcesFactory, serve_stdio};
pub use sources::{DiskSources, RouterSources, Sources, pick};
pub use tools::version::VersionInfo;
