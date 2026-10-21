#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
// Build the dashboard with Vite and write every file the huddle binary embeds. The standalone
// kanban page builds beside it into internal/kanban/web/dist. Plain mode writes the committed
// artifacts; --check rebuilds in memory and byte-compares against what is committed, so a stale
// bundle never ships.
import { build as viteBuild } from "vite";
import { iconFiles } from "./icons.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const check = process.argv.includes("--check");

// The dashboard's outDir, declared in vite.config.ts: the built files are committed next to the web
// package, where the huddle binary embeds them. index.html is not built, so it is neither written here
// nor compared.
const APP_DIR = "../../internal/huddle/web/dist";

// The standalone kanban page's outDir: the board server's binary embeds these beside its API.
const BOARD_DIR = "../../internal/kanban/web/dist";
// The standalone page's own index, written by this script: the board server serves it as it is,
// and the export inlines its one stylesheet link and one script tag.
const STANDALONE_INDEX = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Kanban</title>
    <link rel="stylesheet" href="./app.css" />
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="./app.js"></script>
  </body>
</html>
`;

/**
 * One vite build over the workspace's config. Nothing is written here — the caller writes or
 * compares each file. `over` carries the second build's overrides (entry, output names, limits).
 * @param {object} [over] build options merged over the config's own
 * @returns {Promise<{fileName: string, contents: Buffer}[]>}
 */
async function bundleApp(over = {}) {
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
      build: { write: false, ...over },
    });
  } finally {
    if (was === undefined) delete process.env.NODE_ENV;
    else process.env.NODE_ENV = was;
  }
  const outputs = (Array.isArray(result) ? result : [result]).flatMap((r) => r.output);
  return outputs.map((o) => ({
    fileName: o.fileName,
    contents: Buffer.from(o.type === "chunk" ? o.code : o.source),
  }));
}

let failures = 0;

/** Collect every file under one directory, named relative to it; a missing directory lists empty
 *  (the ship comparisons above have already named every file the build owes). */
function listFiles(dir, prefix = "") {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return [];
  }
  const listed = [];
  for (const entry of entries) {
    const name = prefix === "" ? entry.name : `${prefix}/${entry.name}`;
    if (entry.isDirectory()) listed.push(...listFiles(join(dir, entry.name), name));
    else listed.push(name);
  }
  return listed;
}

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
for (const name of ["app.js", "app.css"]) {
  if (!appFiles.some((f) => f.fileName === name)) throw new Error(`vite produced no ${name}`);
}
for (const { fileName, contents } of appFiles) {
  await ship(join(APP_DIR, fileName), contents);
}

// The standalone kanban page: the hub's board code over the standalone entry, one script and one
// stylesheet. Every font and image inlines into app.css, so the exported page reaches no network;
// any other asset file is a build failure.
const boardFiles = await bundleApp({
  outDir: "../../internal/kanban/web/dist",
  emptyOutDir: false,
  assetsInlineLimit: () => true,
  rolldownOptions: {
    input: "src/kanban/standalone.tsx",
    output: {
      format: "es",
      entryFileNames: "app.js",
      // every stylesheet the entry pulls in folds into app.css; anything else inlines
      assetFileNames: (asset) =>
        asset.names.some((name) => name.endsWith(".css")) ? "app.css" : "[name][extname]",
      codeSplitting: false,
    },
  },
});
for (const name of ["app.js", "app.css"]) {
  if (!boardFiles.some((f) => f.fileName === name)) throw new Error(`vite produced no standalone ${name}`);
}
const extra = boardFiles.filter((f) => f.fileName !== "app.js" && f.fileName !== "app.css");
if (extra.length > 0) {
  throw new Error(
    `the standalone page must inline every asset, got separate files: ${extra.map((f) => f.fileName).join(", ")}`,
  );
}
// plain mode clears the embed folder first, so a renamed hash never leaves a stale file behind;
// check mode fails below on a committed file the build no longer produces
if (!check) rmSync(resolve(root, BOARD_DIR), { recursive: true, force: true });
for (const { fileName, contents } of boardFiles) {
  await ship(join(BOARD_DIR, fileName), contents);
}
await ship(join(BOARD_DIR, "index.html"), Buffer.from(STANDALONE_INDEX));
if (check) {
  const produced = new Set([...boardFiles.map((f) => f.fileName), "index.html"]);
  const board = resolve(root, BOARD_DIR);
  for (const name of listFiles(board)) {
    if (!produced.has(name)) {
      failures += 1;
      console.error(`stale artifact: ${join(BOARD_DIR, name)} — run: bun run build`);
    }
  }
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
