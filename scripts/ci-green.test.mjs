// scripts/ci-green.mjs refuses while the last completed main CI run is not green. Every case runs the script as a
// child process with a fake `gh` first on PATH that records its arguments and answers a fixture, so nothing here
// reaches GitHub. Run with `node --test scripts/ci-green.test.mjs`.
import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, it } from "node:test";

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), "ci-green.mjs");

const FAKE_GH = `#!/bin/sh
printf '%s\\n' "$*" >> "$GH_LOG"
if [ -n "\${GH_STDERR:-}" ]; then
  printf '%s\\n' "$GH_STDERR" >&2
  exit "\${GH_EXIT:-1}"
fi
printf '%s\\n' "$GH_JSON"
`;

let dir;
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "ci-green-"));
});
afterEach(() => {
  rmSync(dir, { recursive: true, force: true });
});

/** Runs the guard with a fake gh on PATH (or none) and returns its status, output and the gh calls it made. */
function guard({ json = "[]", stderr = "", withGh = true } = {}) {
  const bin = join(dir, "bin");
  const log = join(dir, "gh.log");
  mkdirSync(bin, { recursive: true });
  writeFileSync(log, "");
  if (withGh) {
    writeFileSync(join(bin, "gh"), FAKE_GH);
    chmodSync(join(bin, "gh"), 0o755);
  }
  const result = spawnSync(process.execPath, [SCRIPT], {
    encoding: "utf8",
    env: { PATH: bin, HOME: dir, GH_LOG: log, GH_JSON: json, GH_STDERR: stderr },
  });
  return { status: result.status, stdout: result.stdout, stderr: result.stderr, calls: readFileSync(log, "utf8") };
}

const run = (conclusion) =>
  JSON.stringify([{ conclusion, databaseId: 41314513918, headSha: "d531913f0c2a9b7e4a1d5c6e8f9a0b1c2d3e4f50" }]);

describe("ci-green", () => {
  it("passes when the last completed main CI run succeeded", () => {
    const r = guard({ json: run("success") });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /41314513918/);
  });

  it("refuses when the last completed main CI run failed, naming the run", () => {
    const r = guard({ json: run("failure") });
    assert.equal(r.status, 1);
    assert.match(r.stderr, /41314513918/);
    assert.match(r.stderr, /d531913f/);
    assert.match(r.stderr, /failure/);
  });

  it("refuses when the last completed main CI run was cancelled", () => {
    const r = guard({ json: run("cancelled") });
    assert.equal(r.status, 1);
    assert.match(r.stderr, /41314513918/);
    assert.match(r.stderr, /cancelled/);
  });

  it("refuses when the last completed main CI run timed out", () => {
    const r = guard({ json: run("timed_out") });
    assert.equal(r.status, 1);
    assert.match(r.stderr, /timed_out/);
  });

  it("asks gh only for the last completed ci.yml run on main, a read", () => {
    const r = guard({ json: run("success") });
    assert.equal(
      r.calls.trim(),
      "run list --branch main --workflow ci.yml --status completed -L 1 --json conclusion,databaseId,headSha",
    );
  });

  it("fails closed with one line when gh is not installed", () => {
    const r = guard({ withGh: false });
    assert.equal(r.status, 1);
    assert.equal(r.stderr.trim().split("\n").length, 1);
    assert.match(r.stderr, /gh is not installed/);
  });

  it("fails closed with one line when gh cannot reach GitHub", () => {
    const r = guard({ stderr: "error connecting to api.github.com\ncheck your internet connection" });
    assert.equal(r.status, 1);
    assert.equal(r.stderr.trim().split("\n").length, 1);
    assert.match(r.stderr, /error connecting to api\.github\.com/);
  });

  it("fails closed when main has no completed ci.yml run", () => {
    const r = guard({ json: "[]" });
    assert.equal(r.status, 1);
    assert.match(r.stderr, /no completed/);
  });

  it("fails closed when gh answers something that is not a run", () => {
    for (const json of ["not json", "{}", '[{"conclusion":"success"}]', '[{"databaseId":1,"headSha":"abc"}]']) {
      const r = guard({ json });
      assert.equal(r.status, 1, json);
      assert.equal(r.stderr.trim().split("\n").length, 1, json);
    }
  });
});
