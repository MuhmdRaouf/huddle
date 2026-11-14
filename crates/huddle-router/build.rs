//! Joins the dashboard files into one page, minifies it when esbuild is present, and gzips it.
//!
//! Writes `page.html` (the joined, unminified page the tests read) and `page.html.gz` (the bytes
//! the router serves) into `OUT_DIR`. The served page is the joined page with its CSS and JS run
//! through esbuild `--minify`. Without esbuild a plain build serves the unminified page and warns;
//! with `HUDDLE_REQUIRE_MINIFY` set (CI release builds) a missing esbuild stops the build.
//! `ESBUILD` names the binary, else `esbuild` is looked up on `PATH`.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use flate2::Compression;
use flate2::write::GzEncoder;

#[path = "build_support/minify_plan.rs"]
mod minify_plan;
use minify_plan::{MinifyPlan, plan};

const HEAD: &str = concat!(
    "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n",
    "<meta name=\"viewport\" content=\"width=device-width, initial-scale=1, viewport-fit=cover\">\n",
    "<title>Huddle Admin</title>\n<style>\n",
);
const AFTER_STYLE: &str = "</style>\n</head>\n<body>\n";
const SCRIPT_OPEN: &str = "<script>\n";
const TAIL: &str = "</script>\n</body>\n</html>\n";
/// The script's wrapper: one function scope in strict mode, so esbuild also renames the
/// script's own top-level names.
const WRAP_OPEN: &str = "(function(){\n\"use strict\";\n";
const WRAP_CLOSE: &str = "})();\n";

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

/// Joins the page from the CSS, the markup and the script, each as the caller hands it over.
fn join(css: &str, markup: &str, script: &str) -> String {
    [HEAD, css, AFTER_STYLE, markup, SCRIPT_OPEN, script, TAIL].concat()
}

fn esbuild_binary() -> String {
    std::env::var("ESBUILD").unwrap_or_else(|_| "esbuild".to_string())
}

/// Runs `source` through `esbuild --minify` as the given loader (`css` or `js`).
fn minify(esbuild: &str, loader: &str, source: &str) -> Result<String, String> {
    let mut child = Command::new(esbuild)
        .args(["--minify", &format!("--loader={loader}")])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("start {esbuild}: {e}"))?;
    let mut stdin = child.stdin.take().ok_or("esbuild stdin")?;
    let input = source.to_owned();
    let writer = std::thread::spawn(move || stdin.write_all(input.as_bytes()));
    let output = child
        .wait_with_output()
        .map_err(|e| format!("wait for {esbuild}: {e}"))?;
    writer
        .join()
        .map_err(|_| "esbuild stdin writer".to_string())?
        .map_err(|e| format!("write to {esbuild}: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "{esbuild} --loader={loader}: {}",
            String::from_utf8_lossy(&output.stderr)
        ));
    }
    String::from_utf8(output.stdout).map_err(|e| format!("{esbuild} output: {e}"))
}

fn esbuild_answers(esbuild: &str) -> bool {
    Command::new(esbuild)
        .arg("--version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|s| s.success())
}

fn main() {
    let dir = Path::new("src/dashboard");
    println!("cargo:rerun-if-changed=src/dashboard");
    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-changed=build_support/minify_plan.rs");
    println!("cargo:rerun-if-env-changed=ESBUILD");
    println!("cargo:rerun-if-env-changed=HUDDLE_REQUIRE_MINIFY");
    println!("cargo:rerun-if-env-changed=PATH");

    let css = read(dir, "style.css");
    let markup = read(dir, "index.html");
    let body: String = SCRIPTS.iter().map(|name| read(dir, name)).collect();
    let script = [WRAP_OPEN, &body, WRAP_CLOSE].concat();
    let page = join(&css, &markup, &script);

    let esbuild = esbuild_binary();
    let required = std::env::var_os("HUDDLE_REQUIRE_MINIFY").is_some();
    let served = match plan(esbuild_answers(&esbuild), required) {
        MinifyPlan::Minify => {
            let css = minify(&esbuild, "css", &css).unwrap_or_else(|e| panic!("{e}"));
            let script = minify(&esbuild, "js", &script).unwrap_or_else(|e| panic!("{e}"));
            join(&css, &markup, &script)
        }
        MinifyPlan::PlainWithWarning => {
            println!("cargo:warning=esbuild not found: the dashboard page is embedded unminified");
            page.clone()
        }
        MinifyPlan::Fail => panic!("HUDDLE_REQUIRE_MINIFY is set but esbuild is not available"),
    };

    let mut gz = GzEncoder::new(Vec::new(), Compression::best());
    gz.write_all(served.as_bytes()).expect("gzip the page");
    let gzipped = gz.finish().expect("finish gzip");

    let out = PathBuf::from(std::env::var("OUT_DIR").expect("OUT_DIR"));
    fs::write(out.join("page.html"), page).expect("write page.html");
    fs::write(out.join("page.html.gz"), gzipped).expect("write page.html.gz");
}
