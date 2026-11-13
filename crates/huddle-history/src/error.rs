use std::fmt;
use std::io;

/// Everything the history store can fail on.
#[derive(Debug)]
pub enum HistoryError {
    Io(io::Error),
    Sqlite(rusqlite::Error),
    /// The file was written by a newer huddle than this one.
    SchemaTooNew {
        found: i64,
        supported: i64,
    },
    /// A bucket width of zero milliseconds.
    ZeroWidth,
    /// A scan thread panicked.
    Worker,
}

impl fmt::Display for HistoryError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            HistoryError::Io(err) => write!(f, "history file: {err}"),
            HistoryError::Sqlite(err) => write!(f, "history database: {err}"),
            HistoryError::SchemaTooNew { found, supported } => write!(
                f,
                "history schema version {found} is newer than the supported version {supported}"
            ),
            HistoryError::ZeroWidth => write!(f, "bucket width must be at least 1 ms"),
            HistoryError::Worker => write!(f, "history scan stopped unexpectedly"),
        }
    }
}

impl std::error::Error for HistoryError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            HistoryError::Io(err) => Some(err),
            HistoryError::Sqlite(err) => Some(err),
            _ => None,
        }
    }
}

impl From<io::Error> for HistoryError {
    fn from(err: io::Error) -> Self {
        HistoryError::Io(err)
    }
}

impl From<rusqlite::Error> for HistoryError {
    fn from(err: rusqlite::Error) -> Self {
        HistoryError::Sqlite(err)
    }
}
