//! Request history in SQLite: one row per finished request, written off the caller's thread,
//! filled with usage later, and queried for the dashboard. The crate takes a path and plain values;
//! it knows nothing of the router, state folders or prices.

mod error;
mod history;
mod query;
mod row;
mod schema;
mod writer;

pub use error::HistoryError;
pub use history::History;
pub use query::{Bucket, Filter, GroupBy, ModelUsage, Page, SearchResult, StatusClass, Total};
pub use row::{Credit, Range, Row};
pub use schema::{COLUMNS, VERSION};
pub use writer::Writer;
