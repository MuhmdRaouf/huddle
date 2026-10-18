#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
// Build the dashboard with Vite and write every file the huddle binary embeds. Plain mode writes
// the committed artifacts; --check rebuilds in memory and byte-compares against what is committed,
// so a stale bundle never ships.
import { build as viteBuild } from "vite";
import { iconFiles } from "./icons.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const check = process.argv.includes("--check");

// The dashboard's outDir, declared in vite.config.ts: the built files are committed next to the web
// package, where the huddle binary embeds them. index.html is not built, so it is neither written here
// nor compared.
const APP_DIR = "../../internal/huddle/web/dist";

/**
 * The dashboard: vite.config.ts names the entry; one build yields app.js, app.css and the bundled fonts
 * under APP_DIR. Nothing is written here — the caller writes or compares each file.
 * @returns {Promise<{fileName: string, contents: Buffer}[]>}
 */
async function bundleApp() {
  // always the production build: a test run's NODE_ENV=test must not change the committed bytes
  const was = process.env.NODE_ENV;
  process.env.NODE_ENV = "production";
  let result;
  try {
    result = await viteBuild({
      configFile: resolve(root, "vite.config.ts"),
      root,
      mode: "production",
      logLevel: "silent",
      build: { write: false },
    });
  } finally {
    if (was === undefined) delete process.env.NODE_ENV;
    else process.env.NODE_ENV = was;
  }
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
const appFiles = await bundleApp();
for (const { fileName, contents } of appFiles) {
  await ship(join(APP_DIR, fileName), contents);
}

// The theme boot script index.html loads before anything paints; a file of its own because the CSP the
// hub serves allows no inline script. Shipped as written, so the check catches any drift too.
const themeBoot = readFileSync(resolve(root, "public/theme-boot.js"));
await ship(join(APP_DIR, "theme-boot.js"), themeBoot);

// The PWA: the icons rasterised from the logo and the manifest, shipped as written. The service
// worker follows, its cache revision stamped with sha256 over every shell byte (the worker's own
// bytes aside), so a changed shell installs a new cache beside the old one instead of serving it
// stale.
const pwaFiles = [
  ...iconFiles(),
  { fileName: "manifest.webmanifest", contents: readFileSync(resolve(root, "public/manifest.webmanifest")) },
];
for (const { fileName, contents } of pwaFiles) {
  await ship(join(APP_DIR, fileName), contents);
}

// The landing page's tour pictures, shipped as written under tour/: small committed screenshots
// the "see how it works" strip and its lightbox load.
for (const name of readdirSync(resolve(root, "public/tour"))) {
  await ship(join(APP_DIR, "tour", name), readFileSync(resolve(root, "public/tour", name)));
}

const revision = createHash("sha256");
for (const bytes of [
  ...appFiles.map((f) => f.contents),
  themeBoot,
  ...pwaFiles.map((f) => f.contents),
  readFileSync(resolve(root, APP_DIR, "index.html")),
]) {
  revision.update(bytes);
}
await ship(
  join(APP_DIR, "sw.js"),
  Buffer.from(
    readFileSync(resolve(root, "public/sw.js")).toString().replaceAll("@@REV@@", revision.digest("hex")),
    "utf8",
  ),
);

if (check && failures > 0) process.exit(1);
