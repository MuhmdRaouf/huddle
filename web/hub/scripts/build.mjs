#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { brotliCompressSync, brotliDecompressSync, constants, gunzipSync, gzipSync } from "node:zlib";
// Build the dashboard with Vite and write every file the huddle binary embeds. The standalone
// kanban page builds beside it into internal/kanban/web/dist. Plain mode writes the committed
// artifacts; --check rebuilds in memory and byte-compares against what is committed, so a stale
// bundle never ships. Every hashed script and stylesheet ships beside its .br and .gz, so the hub
// answers them precompressed.
import { build as viteBuild } from "vite";
import { iconFiles } from "./icons.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const check = process.argv.includes("--check");

// The dashboard's outDir, declared in vite.config.ts: the built files are committed next to the web
// package, where the huddle binary embeds them. This script writes everything in it, index.html
// included, so plain mode starts from an empty folder and check mode flags any file it did not make.
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

/** Every artifact path this run wrote or compared, so check mode can name the ones it no longer makes. */
const shipped = new Set();

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
  shipped.add(path);
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

/** The eight-character content hash a file shipped as written carries in its name, the shape vite's
 *  [hash] takes. */
function contentHash(bytes) {
  return createHash("sha256").update(bytes).digest("base64url").slice(0, 8);
}

/** The two encodings a browser may ask for, each with its decoder: compression levels and the zlib
 *  build move the bytes from one machine to the next, so check mode compares what they decode to. */
const ENCODINGS = [
  {
    ext: ".br",
    encode: (bytes) =>
      brotliCompressSync(bytes, {
        params: {
          [constants.BROTLI_PARAM_QUALITY]: constants.BROTLI_MAX_QUALITY,
          [constants.BROTLI_PARAM_SIZE_HINT]: bytes.length,
        },
      }),
    decode: brotliDecompressSync,
  },
  { ext: ".gz", encode: (bytes) => gzipSync(bytes, { level: 9 }), decode: gunzipSync },
];

/** Writes one hashed asset and its precompressed copies; check mode decodes each committed copy and
 *  compares it with the asset's bytes. */
async function shipHashed(outfile, contents) {
  await ship(outfile, contents);
  for (const { ext, encode, decode } of ENCODINGS) {
    const path = resolve(root, outfile + ext);
    shipped.add(path);
    if (check) {
      let decoded = null;
      try {
        decoded = decode(readFileSync(path));
      } catch {
        // reported below like any other mismatch
      }
      if (decoded === null || !decoded.equals(contents)) {
        failures += 1;
        console.error(`stale bundle: ${outfile + ext} — run: bun run build`);
      }
      continue;
    }
    const encoded = encode(contents);
    writeFileSync(path, encoded, { mode: 0o644 });
    console.log(`built ${outfile + ext} (${encoded.length} bytes)`);
  }
}

/** Fails check mode on every file under dir this run did not make. */
function flagStale(dir) {
  for (const name of listFiles(resolve(root, dir))) {
    if (!shipped.has(resolve(root, dir, name))) {
      failures += 1;
      console.error(`stale artifact: ${join(dir, name)} — run: bun run build`);
    }
  }
}

// plain mode clears the embed folder first, so a renamed hash never leaves a stale file behind;
// check mode fails at the end on a committed file the build no longer produces
if (!check) rmSync(resolve(root, APP_DIR), { recursive: true, force: true });

// The dashboard: one vite build covers app.<hash>.js and app.<hash>.css, and nothing else — an
// asset that escapes the stylesheet as a file of its own is a build failure, the same rule the
// standalone page keeps.
const appFiles = await bundleApp();
const appScript = appFiles.find((f) => /^app\.[^/]+\.js$/.test(f.fileName));
const appStyle = appFiles.find((f) => /^app\.[^/]+\.css$/.test(f.fileName));
if (appScript === undefined || appStyle === undefined)
  throw new Error("vite produced no app script or stylesheet");
const appExtra = appFiles.filter((f) => f !== appScript && f !== appStyle);
if (appExtra.length > 0) {
  throw new Error(
    `the dashboard must inline every asset, got separate files: ${appExtra.map((f) => f.fileName).join(", ")}`,
  );
}
for (const file of [appScript, appStyle]) {
  await shipHashed(join(APP_DIR, file.fileName), file.contents);
}

// The standalone kanban page: the hub's board code over the standalone entry, one script and one
// stylesheet. Assets inline into app.css, so the exported page reaches no network; an asset left
// as its own file is a build failure.
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
// plain mode clears the embed folder first, so a renamed hash never leaves a stale file behind
if (!check) rmSync(resolve(root, BOARD_DIR), { recursive: true, force: true });
for (const { fileName, contents } of boardFiles) {
  await ship(join(BOARD_DIR, fileName), contents);
}
await ship(join(BOARD_DIR, "index.html"), Buffer.from(STANDALONE_INDEX));

// The theme boot script index.html loads before anything paints; a file of its own because the CSP the
// hub serves allows no inline script. Shipped as written under its content hash.
const themeBoot = readFileSync(resolve(root, "public/theme-boot.js"));
const themeBootName = `theme-boot.${contentHash(themeBoot)}.js`;
await shipHashed(join(APP_DIR, themeBootName), themeBoot);

// The shell page: index.html as written, each /static/ name it loads swapped for the hashed one.
const assets = {
  "/static/theme-boot.js": `/static/${themeBootName}`,
  "/static/app.css": `/static/${appStyle.fileName}`,
  "/static/app.js": `/static/${appScript.fileName}`,
};
let indexPage = readFileSync(resolve(root, "index.html"), "utf8");
for (const [plain, hashed] of Object.entries(assets)) {
  if (!indexPage.includes(`"${plain}"`)) throw new Error(`index.html loads no ${plain}`);
  indexPage = indexPage.replaceAll(`"${plain}"`, `"${hashed}"`);
}
const indexBytes = Buffer.from(indexPage, "utf8");
await ship(join(APP_DIR, "index.html"), indexBytes);

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
  indexBytes,
]) {
  revision.update(bytes);
}
// the worker precaches the hashed names index.html loads, in the order the page loads them
await ship(
  join(APP_DIR, "sw.js"),
  Buffer.from(
    readFileSync(resolve(root, "public/sw.js"))
      .toString()
      .replaceAll("@@REV@@", revision.digest("hex"))
      .replace(
        '"@@ASSETS@@"',
        Object.values(assets)
          .map((path) => JSON.stringify(path))
          .join(", "),
      ),
    "utf8",
  ),
);

if (check) {
  flagStale(APP_DIR);
  flagStale(BOARD_DIR);
}
if (check && failures > 0) process.exit(1);
