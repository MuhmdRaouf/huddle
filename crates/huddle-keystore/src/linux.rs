//! huddle-keystore: linux.
//!
//! The provider key in the freedesktop Secret Service, through `secret-tool`.

use crate::port::{
    ACCOUNT, Keystore, KeystoreError, OsStore, RunResult, SecretString, ToolRunner,
    assert_valid_key, service_name,
};

/// The Secret Service's tool.
pub const SECRET_TOOL: &str = "secret-tool";

/// The store's name in failures; the name never carries the key.
const LABEL: &str = "Secret Service";

/// The provider key in the freedesktop Secret Service. The key travels only on stdin; a failure
/// names the exit and never the key.
pub struct LinuxStore {
    runner: Box<dyn ToolRunner>,
}

impl LinuxStore {
    pub fn new(runner: Box<dyn ToolRunner>) -> LinuxStore {
        LinuxStore { runner }
    }

    /// The lookup command; reads and the availability probe use it.
    fn lookup(&self, provider: &str) -> RunResult {
        let service = service_name(provider);
        self.runner.run(
            SECRET_TOOL,
            &["lookup", "service", service.as_str(), "account", ACCOUNT],
            None,
        )
    }
}

/// The store's Secret Service label. Display names are provider data the catalog owns; the store
/// has only the provider name.
fn label_for(provider: &str) -> String {
    format!("{provider} API key")
}

/// The failure's cause from a tool result: the exit code or the missing tool, never the tool's
/// output and never the key.
fn why(result: &RunResult) -> String {
    if result.missing {
        "tool not found".to_string()
    } else if let Some(code) = result.code {
        format!("exit {code}")
    } else {
        "signal".to_string()
    }
}

fn tool_failure(action: &'static str, result: &RunResult) -> KeystoreError {
    KeystoreError::Tool {
        label: LABEL,
        action,
        why: why(result),
    }
}

/// The value with one trailing newline removed.
fn trimmed(stdout: &str) -> &str {
    stdout.strip_suffix('\n').unwrap_or(stdout)
}

impl Keystore for LinuxStore {
    fn get(&self, provider: &str) -> Result<Option<SecretString>, KeystoreError> {
        let result = self.lookup(provider);
        if !result.exited(0) {
            return Ok(None);
        }
        let value = trimmed(&result.stdout);
        if value.is_empty() {
            Ok(None)
        } else {
            Ok(Some(SecretString::new(value.to_string())))
        }
    }

    fn set(&self, provider: &str, key: &SecretString) -> Result<(), KeystoreError> {
        let key = key.expose();
        assert_valid_key(key)?;
        let service = service_name(provider);
        // No trailing newline: secret-tool stores stdin up to EOF as it is.
        let result = self.runner.run(
            SECRET_TOOL,
            &[
                "store",
                &format!("--label={}", label_for(provider)),
                "service",
                service.as_str(),
                "account",
                ACCOUNT,
            ],
            Some(key),
        );
        if !result.exited(0) {
            return Err(tool_failure("store", &result));
        }
        Ok(())
    }

    fn delete(&self, provider: &str) -> Result<(), KeystoreError> {
        let service = service_name(provider);
        let result = self.runner.run(
            SECRET_TOOL,
            &["clear", "service", service.as_str(), "account", ACCOUNT],
            None,
        );
        if result.missing {
            return Err(tool_failure("remove", &result));
        }
        Ok(())
    }
}

impl OsStore for LinuxStore {
    fn available(&self) -> bool {
        // Any provider's name proves the bus answers: exit 0, or exit 1 with nothing on stderr,
        // which is "no such secret". A D-Bus or "no secret service" failure says why on stderr.
        let result = self.lookup("zai");
        !result.missing
            && (result.exited(0) || (result.exited(1) && result.stderr.trim().is_empty()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::port::{Keystore, KeystoreError, OsStore, RunResult, SecretString, ToolRunner};
    use std::sync::{Arc, Mutex};

    /// One tool call: the program, its argv and the stdin the store handed it.
    type Call = (String, Vec<String>, Option<String>);

    #[derive(Clone, Default)]
    struct Fake {
        calls: Arc<Mutex<Vec<Call>>>,
        answers: Arc<Mutex<Vec<RunResult>>>,
    }
    impl Fake {
        fn answering(answers: Vec<RunResult>) -> Fake {
            Fake {
                answers: Arc::new(Mutex::new(answers)),
                ..Fake::default()
            }
        }
        fn calls(&self) -> Vec<Call> {
            self.calls.lock().expect("calls").clone()
        }
    }
    impl ToolRunner for Fake {
        fn run(&self, program: &str, args: &[&str], stdin: Option<&str>) -> RunResult {
            self.calls.lock().expect("calls").push((
                program.to_string(),
                args.iter().map(|a| a.to_string()).collect(),
                stdin.map(str::to_string),
            ));
            let mut answers = self.answers.lock().expect("answers");
            if answers.is_empty() {
                return ok("");
            }
            answers.remove(0)
        }
    }

    fn ok(stdout: &str) -> RunResult {
        RunResult {
            code: Some(0),
            stdout: stdout.to_string(),
            stderr: String::new(),
            missing: false,
        }
    }
    fn exited(code: i32, stderr: &str) -> RunResult {
        RunResult {
            code: Some(code),
            stdout: String::new(),
            stderr: stderr.to_string(),
            missing: false,
        }
    }
    fn store(fake: &Fake) -> LinuxStore {
        LinuxStore::new(Box::new(fake.clone()))
    }

    #[test]
    fn a_read_asks_the_lookup_attributes() {
        let fake = Fake::answering(vec![ok("abcdefghijklmnop\n")]);
        assert_eq!(
            store(&fake)
                .get("kimi")
                .expect("get")
                .expect("key")
                .expose(),
            "abcdefghijklmnop"
        );
        assert_eq!(fake.calls()[0].0, "secret-tool");
        assert_eq!(
            fake.calls()[0].1,
            vec!["lookup", "service", "kimi-plugin-cc", "account", "api-key"]
        );
    }

    #[test]
    fn a_read_that_fails_is_no_key() {
        let fake = Fake::answering(vec![exited(1, "")]);
        assert!(store(&fake).get("kimi").expect("get").is_none());
    }

    #[test]
    fn a_set_stores_the_key_on_stdin_with_a_label_and_no_newline() {
        let key = SecretString::new("abcdefghijklmnop".to_string());
        let fake = Fake::default();
        store(&fake).set("kimi", &key).expect("set");
        let calls = fake.calls();
        assert_eq!(
            calls[0].1,
            vec![
                "store",
                "--label=kimi API key",
                "service",
                "kimi-plugin-cc",
                "account",
                "api-key"
            ]
        );
        assert_eq!(calls[0].2.as_deref(), Some("abcdefghijklmnop"));
        assert!(
            calls[0]
                .1
                .iter()
                .all(|arg| !arg.contains("abcdefghijklmnop"))
        );
    }

    #[test]
    fn a_failed_store_names_the_exit_not_the_key() {
        let key = SecretString::new("abcdefghijklmnop".to_string());
        let fake = Fake::answering(vec![exited(3, "dbus went away")]);
        let error = store(&fake).set("kimi", &key).expect_err("must fail");
        assert!(matches!(error, KeystoreError::Tool { .. }));
        assert_eq!(
            error.to_string(),
            "could not store the key in Secret Service (exit 3)"
        );
    }

    #[test]
    fn a_delete_clears_the_item_and_only_fails_when_the_tool_is_missing() {
        let fake = Fake::default();
        store(&fake).delete("kimi").expect("delete");
        assert_eq!(
            fake.calls()[0].1,
            vec!["clear", "service", "kimi-plugin-cc", "account", "api-key"]
        );
        let missing = RunResult {
            code: None,
            stdout: String::new(),
            stderr: String::new(),
            missing: true,
        };
        let fake = Fake::answering(vec![missing]);
        assert!(store(&fake).delete("kimi").is_err());
    }

    #[test]
    fn available_answers_for_a_lookup_of_a_key_that_is_not_there_but_not_for_a_dead_bus() {
        assert!(store(&Fake::answering(vec![ok("x")])).available());
        assert!(store(&Fake::answering(vec![exited(1, "")])).available());
        assert!(!store(&Fake::answering(vec![exited(1, "Cannot autolaunch D-Bus")])).available());
        let missing = RunResult {
            code: None,
            stdout: String::new(),
            stderr: String::new(),
            missing: true,
        };
        assert!(!store(&Fake::answering(vec![missing])).available());
    }
}
