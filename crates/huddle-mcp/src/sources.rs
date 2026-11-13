use std::path::PathBuf;

/// Where a tool reads its data. Tools never touch the disk or the router themselves.
pub trait Sources: Send {
    /// Whether the router answered its health probe when these sources were picked.
    fn router_up(&self) -> bool;
    /// The path of the running binary.
    fn binary_path(&self) -> PathBuf;
}

/// Reads state files only. Used while the router is down.
pub struct DiskSources {
    pub binary: PathBuf,
}

impl Sources for DiskSources {
    fn router_up(&self) -> bool {
        false
    }

    fn binary_path(&self) -> PathBuf {
        self.binary.clone()
    }
}

/// Reads state files and asks the live router for what only it holds.
pub struct RouterSources {
    pub binary: PathBuf,
}

impl Sources for RouterSources {
    fn router_up(&self) -> bool {
        true
    }

    fn binary_path(&self) -> PathBuf {
        self.binary.clone()
    }
}

/// The router's sources while it answers, the disk's otherwise.
pub fn pick(router_answers: bool, binary: PathBuf) -> Box<dyn Sources> {
    if router_answers {
        Box::new(RouterSources { binary })
    } else {
        Box::new(DiskSources { binary })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pick_follows_the_health_answer() {
        assert!(pick(true, PathBuf::from("huddle")).router_up());
        assert!(!pick(false, PathBuf::from("huddle")).router_up());
    }
}
