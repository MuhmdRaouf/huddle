use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Stdio};

use huddle_testkit::temp_home::TempHome;
use serde_json::{Value, json};

const VERSION_FILE: &str = include_str!("../../../VERSION");

struct Session {
    child: Child,
    reader: BufReader<std::process::ChildStdout>,
    lines: Vec<String>,
}

impl Session {
    fn start(home: &TempHome) -> Session {
        let mut child = home
            .command(env!("CARGO_BIN_EXE_huddle"))
            .arg("mcp")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let reader = BufReader::new(child.stdout.take().unwrap());
        Session {
            child,
            reader,
            lines: Vec::new(),
        }
    }

    fn send(&mut self, message: Value) {
        let stdin = self.child.stdin.as_mut().unwrap();
        writeln!(stdin, "{message}").unwrap();
        stdin.flush().unwrap();
    }

    fn answer(&mut self) -> Value {
        let mut line = String::new();
        self.reader.read_line(&mut line).unwrap();
        self.lines.push(line.clone());
        serde_json::from_str(&line).unwrap()
    }

    fn call(&mut self, message: Value) -> Value {
        self.send(message);
        self.answer()
    }

    fn handshake(&mut self) -> Value {
        let init = self.call(json!({
            "jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "m1-test", "version": "0"}
            }
        }));
        self.send(json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
        init
    }

    fn finish(mut self) -> Vec<String> {
        drop(self.child.stdin.take());
        let _ = self.child.wait();
        self.lines
    }
}

fn version_answer(session: &mut Session) -> Value {
    let answer = session.call(json!({
        "jsonrpc": "2.0", "id": 3, "method": "tools/call",
        "params": {"name": "huddle_version", "arguments": {}}
    }));
    answer["result"]["structuredContent"].clone()
}

#[test]
fn m1_stdio_lists_tools_and_answers_a_call() {
    let home = TempHome::new();
    let mut session = Session::start(&home);
    let init = session.handshake();
    assert_eq!(init["result"]["serverInfo"]["name"], "huddle");
    assert_eq!(init["result"]["serverInfo"]["version"], VERSION_FILE.trim());

    let tools = session.call(json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list"}));
    let names: Vec<&str> = tools["result"]["tools"]
        .as_array()
        .unwrap()
        .iter()
        .map(|tool| tool["name"].as_str().unwrap())
        .collect();
    assert!(names.contains(&"huddle_version"), "{names:?}");

    let version = version_answer(&mut session);
    assert_eq!(version["version"], VERSION_FILE.trim());
    assert!(version["binary_path"].as_str().unwrap().contains("huddle"));
    session.finish();
}

#[test]
fn m1_stdio_works_with_the_router_down() {
    let home = TempHome::new();
    let mut session = Session::start(&home);
    session.handshake();
    let version = version_answer(&mut session);
    assert_eq!(version["router_up"], false);
    session.finish();
}

#[test]
fn m1_nothing_but_json_rpc_on_stdout() {
    let home = TempHome::new();
    let mut session = Session::start(&home);
    session.handshake();
    session.call(json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list"}));
    version_answer(&mut session);
    let lines = session.finish();
    assert!(lines.len() >= 3);
    for line in lines {
        let parsed: Value = serde_json::from_str(&line).unwrap();
        assert_eq!(parsed["jsonrpc"], "2.0", "{line}");
    }
}
