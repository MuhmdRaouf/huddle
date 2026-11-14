use huddle_config::mcp_token::McpToken;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::sync::{Arc, Barrier};

#[test]
fn c3_two_ensures_in_one_process_do_not_clash() {
    let home = TempHome::new();
    let path = home.root().join("state/huddle/mcp-token");
    let barrier = Arc::new(Barrier::new(16));
    let threads: Vec<_> = (0..16)
        .map(|_| {
            let (path, barrier) = (path.clone(), barrier.clone());
            std::thread::spawn(move || {
                barrier.wait();
                McpToken::ensure(&path)
            })
        })
        .collect();
    let tokens: Vec<McpToken> = threads
        .into_iter()
        .map(|t| t.join().expect("thread").expect("ensure"))
        .collect();
    assert!(tokens.windows(2).all(|pair| pair[0] == pair[1]));
    assert_eq!(McpToken::read(&path).expect("read"), tokens[0]);
    let mode = fs::metadata(&path).expect("meta").permissions().mode() & 0o777;
    assert_eq!(mode, 0o600);
    let names: Vec<String> = fs::read_dir(path.parent().expect("parent"))
        .expect("dir")
        .flatten()
        .map(|entry| entry.file_name().to_string_lossy().into_owned())
        .collect();
    assert_eq!(names, vec!["mcp-token".to_string()], "no temp file is left");
}
