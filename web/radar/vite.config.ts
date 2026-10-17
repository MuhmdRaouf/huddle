import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import { type PluginOption, defineConfig } from "vite-plus";

// web/ui sits beside this workspace and ships the shared components its sources carry: one alias points
// every @huddle/ui/* import at the sources, so edits land without a build step in between. Preact needs no
// alias: every workspace links the one store copy, and dedupe keeps the bundle at one instance.
const shared = fileURLToPath(new URL("../ui/src", import.meta.url));
const repo = fileURLToPath(new URL("../..", import.meta.url));

const coverage = {
  provider: "v8" as const,
  include: ["src/**/*.{ts,tsx}"],
  // The dashboard's entry point is thin glue around tested logic.
  exclude: ["src/ui/main.tsx", "src/ui/jsx.d.ts"],
  thresholds: { lines: 90, branches: 90, functions: 90, statements: 90 },
};

// The dashboard is one ES module, app.js, and one stylesheet, app.css, built into
// internal/radar/web/dist, where the radar binary embeds them next to the index.html it ships as
// it is: no html entry, no hashed names, no public dir to copy. The CSS entry is src/ui/app.css, imported by
// main.tsx; the @tailwindcss/vite plugin compiles it and the fonts it references land in dist/fonts/.
export default defineConfig({
  oxc: { jsx: { runtime: "automatic", importSource: "preact" } },
  // The plugin is typed against this workspace's own vite (the one scripts/build.mjs runs); the config type
  // comes from vite-plus's bundled vite, so the two copies of the Plugin type need this one bridge.
  plugins: [tailwindcss() as PluginOption],
  resolve: {
    alias: [
      // The dashboard's markdown bundles in from the shared workspace (huddle tests it by the same
      // alias); the alias also carries @huddle/ui/theme.css, which @tailwindcss/vite resolves through
      // vite's resolver.
      { find: /^@huddle\/ui\/(.*)$/, replacement: `${shared}/$1` },
    ],
    dedupe: ["preact"],
  },
  // theme.css pulls its IBM Plex faces from the hoisted node_modules at the repository root.
  server: { fs: { allow: [repo] } },
  publicDir: false,
  build: {
    // Outside this workspace, so emptyOutDir must be explicit for a direct `vite build`; scripts/build.mjs writes
    // the files itself and keeps index.html.
    outDir: "../../internal/radar/web/dist",
    emptyOutDir: true,
    target: "es2022",
    minify: true,
    sourcemap: false,
    modulePreload: false,
    reportCompressedSize: false,
    rolldownOptions: {
      input: "src/ui/main.tsx",
      output: {
        format: "es",
        entryFileNames: "app.js",
        // app.css keeps its exact name (index.html links it); the bundled fonts collect under fonts/.
        assetFileNames: (asset) =>
          asset.names.some((name) => name.endsWith(".css")) ? "app.css" : "fonts/[name][extname]",
        codeSplitting: false,
      },
    },
  },
  lint: {
    ignorePatterns: ["dist/**", "coverage/**", "test/fixtures/**", ".state/**"],
    options: { typeAware: true, typeCheck: true },
    rules: {
      "typescript/no-explicit-any": "error",
      "import/no-default-export": "error",
      "typescript/consistent-type-imports": "error",
      "typescript/no-non-null-assertion": "error",
      complexity: ["error", { max: 12 }],
    },
    overrides: [
      { files: ["vite.config.ts"], rules: { "import/no-default-export": "off" } },
      // A test body is a linear chain of optional-chain assertions and fixture spreads; the rule counts
      // every `?.`, so the same max 12 mis-fires on them. 20 keeps a ceiling without flagging assertions.
      { files: ["test/**"], rules: { complexity: ["error", { max: 20 }] } },
    ],
  },
  fmt: {
    // The fixtures hold golden data and keep what they shipped with.
    ignorePatterns: ["dist/**", "coverage/**", "test/fixtures/**", ".state/**"],
    useTabs: false,
    tabWidth: 2,
    printWidth: 110,
    singleQuote: false,
  },
  test: {
    testTimeout: 60_000,
    coverage,
    projects: [
      // the shared runtime library and the pure UI logic: plain Node
      { extends: true, test: { name: "node", include: ["test/**/*.test.ts"], environment: "node" } },
      // Preact components, rendered into happy-dom
      {
        extends: true,
        test: {
          name: "ui",
          include: ["test/**/*.test.tsx"],
          environment: "happy-dom",
          setupFiles: ["test/ui/setup.ts"],
        },
      },
    ],
  },
});
