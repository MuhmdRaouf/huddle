#!/usr/bin/env node
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
// Build the dashboard with Vite and write every file the radar binary embeds. Plain mode writes
// the committed artifacts; --check rebuilds in memory and byte-compares against what is committed, so a
// stale bundle never ships.
import { build as viteBuild } from "vite";

const root = fileURLToPath(new URL("..", import.meta.url));
const check = process.argv.includes("--check");

// The dashboard's outDir, declared in vite.config.ts: the built files are committed next to the web
// package, where the radar binary embeds them. index.html is not built, so it is neither written here nor
// compared.
const APP_DIR = "../../internal/radar/web/dist";

/**
 * The dashboard: vite.config.ts names the entry; one build yields app.js, app.css and the bundled fonts under
 * APP_DIR. Nothing is written here — the caller writes or compares each file.
 * @returns {Promise<{fileName: string, contents: Buffer}[]>}
 */
async function bundleApp() {
  const result = await viteBuild({
    configFile: resolve(root, "vite.config.ts"),
    root,
    logLevel: "silent",
    build: { write: false },
  });
  const outputs = (Array.isArray(result) ? result : [result]).flatMap((r) => r.output);
  const files = outputs.map((o) => ({
    fileName: o.fileName,
    contents: Buffer.from(o.type === "chunk" ? o.code : o.source),
  }));
  if (!files.some((f) => f.fileName === "app.js")) throw new Error("vite produced no app.js");
  if (!files.some((f) => f.fileName === "app.css")) throw new Error("vite produced no app.css");
  return files;
}

let failures = 0;

/** Write, or byte-compare in check mode, one artifact at `outfile` relative to the workspace root. */
async function ship(outfile, contents, mode = 0o644) {
  const path = resolve(root, outfile);
  if (check) {
    let committed = null;
    try {
      committed = readFileSync(path);
    } catch {
      // reported below like any other mismatch
    }
    if (committed === null || !committed.equals(contents)) {
      failures += 1;
      console.error(`stale bundle: ${outfile} — run: bun run build`);
    }
    return;
  }
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, contents, { mode });
  console.log(`built ${outfile} (${contents.length} bytes)`);
}

// The dashboard: one vite build covers app.js, app.css and every font next to index.html.
for (const { fileName, contents } of await bundleApp()) {
  await ship(join(APP_DIR, fileName), contents);
}

if (check && failures > 0) process.exit(1);
