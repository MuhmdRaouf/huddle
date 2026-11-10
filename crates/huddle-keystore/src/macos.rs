//! huddle-keystore: macos.
//!
//! The provider key in the macOS Keychain, through `/usr/bin/security`.

use crate::port::{
    ACCOUNT, Keystore, KeystoreError, OsStore, RunResult, SecretString, ToolRunner,
    assert_valid_key, service_name,
};

/// The macOS Keychain's tool.
pub const SECURITY: &str = "/usr/bin/security";

/// The store's name in failures; the name never carries the key.
const LABEL: &str = "macOS Keychain";

/// The provider key in the macOS Keychain. Writes and deletes go through `security -i`, which
/// reads its command from stdin, so the key never reaches argv.
pub struct MacosStore {
    runner: Box<dyn ToolRunner>,
}

impl MacosStore {
    pub fn new(runner: Box<dyn ToolRunner>) -> MacosStore {
        MacosStore { runner }
    }

    /// The read command; the write's read-back uses the same one.
    fn read(&self, provider: &str) -> RunResult {
        let service = service_name(provider);
        self.runner.run(
            SECURITY,
            &[
                "find-generic-password",
                "-s",
                service.as_str(),
                "-a",
                ACCOUNT,
                "-w",
            ],
            None,
        )
    }
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

impl Keystore for MacosStore {
    fn get(&self, provider: &str) -> Result<Option<SecretString>, KeystoreError> {
        let result = self.read(provider);
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
        let command =
            format!("add-generic-password -U -s \"{service}\" -a \"{ACCOUNT}\" -w \"{key}\"\n");
        let result = self.runner.run(SECURITY, &["-i"], Some(command.as_str()));
        if !result.exited(0) {
            return Err(tool_failure("store", &result));
        }
        // `security -i` may exit 0 after a failed command: only a read-back proves the write.
        let back = self.read(provider);
        if !back.exited(0) || trimmed(&back.stdout) != key {
            return Err(tool_failure("store", &back));
        }
        Ok(())
    }

    fn delete(&self, provider: &str) -> Result<(), KeystoreError> {
        let service = service_name(provider);
        let command = format!("delete-generic-password -s \"{service}\" -a \"{ACCOUNT}\"\n");
        let result = self.runner.run(SECURITY, &["-i"], Some(command.as_str()));
        if result.missing {
            return Err(tool_failure("remove", &result));
        }
        Ok(())
    }
}

impl OsStore for MacosStore {
    fn available(&self) -> bool {
        let result = self.runner.run(SECURITY, &["default-keychain"], None);
        !result.missing && result.exited(0)
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
    fn failed(code: i32) -> RunResult {
        RunResult {
            code: Some(code),
            stdout: String::new(),
            stderr: "boom".to_string(),
            missing: false,
        }
    }

    fn store(fake: &Fake) -> MacosStore {
        MacosStore::new(Box::new(fake.clone()))
    }

    #[test]
    fn a_read_asks_security_for_the_service_and_account() {
        let fake = Fake::answering(vec![ok("abcdefghijklmnop\n")]);
        assert_eq!(
            store(&fake).get("zai").expect("get").expect("key").expose(),
            "abcdefghijklmnop"
        );
        let calls = fake.calls();
        assert_eq!(calls[0].0, "/usr/bin/security");
        assert_eq!(
            calls[0].1,
            vec![
                "find-generic-password",
                "-s",
                "zai-plugin-cc",
                "-a",
                "api-key",
                "-w"
            ]
        );
    }

    #[test]
    fn a_read_that_fails_is_no_key() {
        let fake = Fake::answering(vec![failed(44)]);
        assert!(store(&fake).get("zai").expect("get").is_none());
    }

    #[test]
    fn a_set_goes_through_stdin_and_never_argv() {
        let key = SecretString::new("abcdefghijklmnop".to_string());
        let fake = Fake::answering(vec![ok(""), ok("abcdefghijklmnop\n")]);
        store(&fake).set("zai", &key).expect("set");
        let calls = fake.calls();
        assert_eq!(calls[0].1, vec!["-i"]);
        assert_eq!(
            calls[0].2.as_deref(),
            Some(
                "add-generic-password -U -s \"zai-plugin-cc\" -a \"api-key\" -w \"abcdefghijklmnop\"\n"
            )
        );
        for (_, args, _) in &calls {
            assert!(
                args.iter().all(|arg| !arg.contains("abcdefghijklmnop")),
                "the key must not reach argv: {args:?}"
            );
        }
    }

    #[test]
    fn a_write_the_read_back_does_not_confirm_fails_without_naming_the_key() {
        let key = SecretString::new("abcdefghijklmnop".to_string());
        let fake = Fake::answering(vec![ok(""), ok("other-value-here\n")]);
        let error = store(&fake).set("zai", &key).expect_err("must fail");
        assert!(matches!(error, KeystoreError::Tool { .. }));
        let text = error.to_string();
        assert!(!text.contains("abcdefghijklmnop"), "{text}");
        assert_eq!(text, "could not store the key in macOS Keychain (exit 0)");
    }

    #[test]
    fn a_set_with_a_bad_shape_runs_no_tool() {
        let fake = Fake::default();
        assert!(matches!(
            store(&fake).set("zai", &SecretString::new("short".to_string())),
            Err(KeystoreError::BadShape)
        ));
        assert!(fake.calls().is_empty());
    }

    #[test]
    fn a_delete_only_fails_when_the_tool_is_missing() {
        let fake = Fake::answering(vec![failed(44)]);
        assert!(store(&fake).delete("zai").is_ok());
        let missing = RunResult {
            code: None,
            stdout: String::new(),
            stderr: String::new(),
            missing: true,
        };
        let fake = Fake::answering(vec![missing]);
        assert_eq!(
            store(&fake).delete("zai").expect_err("missing").to_string(),
            "could not remove the key in macOS Keychain (tool not found)"
        );
    }

    #[test]
    fn available_asks_for_the_default_keychain() {
        let fake = Fake::answering(vec![ok("/x/login.keychain-db\n")]);
        assert!(store(&fake).available());
        assert_eq!(fake.calls()[0].1, vec!["default-keychain"]);
        let fake = Fake::answering(vec![failed(1)]);
        assert!(!store(&fake).available());
    }
}
