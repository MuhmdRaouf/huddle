#!/usr/bin/env node
// Build the board page with Vite and write every file the kanban binary embeds. The page's outDir is
// internal/kanban/web/dist (named in vite.config.ts); plain mode clears that folder and writes every
// emitted file there. --check rebuilds in memory, writes nothing, byte-compares the committed dist —
// a stale or extra file fails — and enforces the size budgets: 60 KiB gzip of JS, 15 KiB gzip of CSS.
// Both sizes print either way.
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";
import { build } from "vite";

const root = fileURLToPath(new URL("..", import.meta.url));
const check = process.argv.includes("--check");
const JS_BUDGET = 60 * 1024;
const CSS_BUDGET = 15 * 1024;

// The page's outDir, declared in vite.config.ts: the built files are committed next to the web
// package, where the kanban binary embeds them.
const APP_DIR = "../../internal/kanban/web/dist";

const result = await build({
  configFile: resolve(root, "vite.config.ts"),
  root,
  logLevel: "silent",
  build: { write: false, emptyOutDir: false },
});
const outputs = (Array.isArray(result) ? result : [result]).flatMap((r) => r.output);
const files = outputs
  .filter((out) => out.type === "chunk" || out.type === "asset")
  .map((out) => ({
    fileName: out.fileName,
    contents: Buffer.from(out.type === "chunk" ? out.code : out.source),
  }));
if (!files.some((f) => f.fileName === "index.html")) throw new Error("vite produced no index.html");

let js = 0;
let css = 0;
for (const { fileName, contents } of files) {
  const gzipped = gzipSync(contents, { level: 9 }).length;
  if (fileName.endsWith(".js")) js += gzipped;
  else if (fileName.endsWith(".css")) css += gzipped;
}

const kb = (n) => `${(n / 1024).toFixed(1)} KiB`;
console.log(
  `kanban bundle: js ${kb(js)} (budget ${kb(JS_BUDGET)}), css ${kb(css)} (budget ${kb(CSS_BUDGET)})`,
);

let failures = 0;

/** Write, or byte-compare in check mode, one artifact at `path` relative to the workspace root. */
function ship(path, contents, mode = 0o644) {
  const target = resolve(root, path);
  if (check) {
    let committed = null;
    try {
      committed = readFileSync(target);
    } catch {
      // reported below like any other mismatch
    }
    if (committed === null || !committed.equals(contents)) {
      failures += 1;
      console.error(`stale artifact: ${path} — run: bun run build`);
    }
    return;
  }
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, contents, { mode });
  console.log(`built ${path} (${contents.length} bytes)`);
}

if (js > JS_BUDGET) {
  console.error(`kanban bundle: js ${kb(js)} exceeds ${kb(JS_BUDGET)}`);
  failures += 1;
}
if (css > CSS_BUDGET) {
  console.error(`kanban bundle: css ${kb(css)} exceeds ${kb(CSS_BUDGET)}`);
  failures += 1;
}

// The page: plain mode clears the embed folder first, so a renamed hash never leaves a stale file
// behind; check mode also fails on a committed file the build no longer produces.
const appDir = resolve(root, APP_DIR);
if (!check) rmSync(appDir, { recursive: true, force: true });
for (const { fileName, contents } of files) {
  ship(join(APP_DIR, fileName), contents);
}

/** Collect every file under one directory, named relative to it. */
function listFiles(dir, prefix = "") {
  const listed = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const name = prefix === "" ? entry.name : `${prefix}/${entry.name}`;
    if (entry.isDirectory()) listed.push(...listFiles(join(dir, entry.name), name));
    else listed.push(name);
  }
  return listed;
}

if (check) {
  const produced = new Set(files.map((f) => f.fileName));
  for (const name of listFiles(appDir)) {
    if (!produced.has(name)) {
      failures += 1;
      console.error(`stale artifact: ${join(APP_DIR, name)} — run: bun run build`);
    }
  }
}

if (failures > 0) process.exit(1);
