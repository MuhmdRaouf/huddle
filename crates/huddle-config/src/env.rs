//! The environment, read once by `main` and passed down.

use std::collections::HashMap;

/// A snapshot of the environment. Only `main` calls `from_process`; everything below it takes
/// `&Env`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Env(HashMap<String, String>);

impl Env {
    pub fn get(&self, k: &str) -> Option<&str> {
        self.0.get(k).map(String::as_str)
    }

    /// Skips a variable whose name or value is not UTF-8 (`std::env::vars` would panic).
    pub fn from_process() -> Env {
        Env(std::env::vars_os()
            .filter_map(|(name, value)| Some((name.into_string().ok()?, value.into_string().ok()?)))
            .collect())
    }

    pub fn with(pairs: &[(&str, &str)]) -> Env {
        Env(pairs
            .iter()
            .map(|(name, value)| (name.to_string(), value.to_string()))
            .collect())
    }

    /// Every variable, in no fixed order.
    pub fn iter(&self) -> impl Iterator<Item = (&str, &str)> + '_ {
        self.0
            .iter()
            .map(|(name, value)| (name.as_str(), value.as_str()))
    }

    /// Every variable as owned pairs, sorted by name.
    pub fn pairs(&self) -> Vec<(String, String)> {
        let mut pairs: Vec<(String, String)> = self.0.clone().into_iter().collect();
        pairs.sort();
        pairs
    }

    /// A copy without the named variables.
    pub fn without(&self, names: &[&str]) -> Env {
        Env(self
            .0
            .iter()
            .filter(|(name, _)| !names.contains(&name.as_str()))
            .map(|(name, value)| (name.clone(), value.clone()))
            .collect())
    }
}
