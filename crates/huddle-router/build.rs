//! Joins the dashboard files into one page and gzips it.
//!
//! Writes `page.html` (the joined page) and `page.html.gz` (the bytes the router serves) into
//! `OUT_DIR`. The joined page is the document the browser receives after gunzipping, so
//! `OUT_DIR/page.html` is also the input for `node --check` on its script.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use flate2::Compression;
use flate2::write::GzEncoder;

const HEAD: &str = concat!(
    "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n",
    "<meta name=\"viewport\" content=\"width=device-width, initial-scale=1, viewport-fit=cover\">\n",
    "<title>Huddle Admin</title>\n<style>\n",
);
const AFTER_STYLE: &str = "</style>\n</head>\n<body>\n";
const SCRIPT_OPEN: &str = "<script>\n(function(){\n\"use strict\";\n";
const TAIL: &str = "})();\n</script>\n</body>\n</html>\n";

/// The script files in the order they join.
const SCRIPTS: [&str; 17] = [
    "core.js",
    "pages/needs-you.js",
    "pages/overview.js",
    "pages/sessions.js",
    "pages/agents.js",
    "pages/requests.js",
    "pages/errors.js",
    "pages/models.js",
    "pages/providers.js",
    "pages/usage.js",
    "pages/projects.js",
    "pages/worktrees.js",
    "pages/jobs.js",
    "pages/harnesses.js",
    "pages/router.js",
    "drawers.js",
    "boot.js",
];

fn read(dir: &Path, name: &str) -> String {
    fs::read_to_string(dir.join(name)).unwrap_or_else(|e| panic!("read dashboard/{name}: {e}"))
}

fn main() {
    let dir = Path::new("src/dashboard");
    println!("cargo:rerun-if-changed=src/dashboard");
    println!("cargo:rerun-if-changed=build.rs");

    let mut page = String::from(HEAD);
    page.push_str(&read(dir, "style.css"));
    page.push_str(AFTER_STYLE);
    page.push_str(&read(dir, "index.html"));
    page.push_str(SCRIPT_OPEN);
    for name in SCRIPTS {
        page.push_str(&read(dir, name));
    }
    page.push_str(TAIL);

    let mut gz = GzEncoder::new(Vec::new(), Compression::best());
    gz.write_all(page.as_bytes()).expect("gzip the page");
    let gzipped = gz.finish().expect("finish gzip");

    let out = PathBuf::from(std::env::var("OUT_DIR").expect("OUT_DIR"));
    fs::write(out.join("page.html"), page).expect("write page.html");
    fs::write(out.join("page.html.gz"), gzipped).expect("write page.html.gz");
}
