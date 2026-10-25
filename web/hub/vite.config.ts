import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import { type PluginOption, defineConfig } from "vite-plus";

// web/ui sits beside this workspace and ships the shared components its sources carry: one alias points
// every @huddle/ui/* import at the sources, so edits land without a build step in between. Preact needs no
// alias: every workspace links the one store copy, and dedupe keeps the bundle at one instance.
const shared = fileURLToPath(new URL("../ui/src", import.meta.url));
const repo = fileURLToPath(new URL("../..", import.meta.url));

// The dashboard is one ES module, app.<hash>.js, and one stylesheet, app.<hash>.css, built into
// internal/huddle/web/dist, where the huddle binary embeds them next to the index.html that
// scripts/build.mjs writes with those names: no html entry, no public dir to copy. A name changes
// whenever its bytes do, so the hub serves every hashed file as immutable. The CSS entry is
// src/app.css, imported by main.tsx; @tailwindcss/vite compiles it and the fonts it references land
// in dist/fonts/, hashed the same way.
export default defineConfig({
  oxc: { jsx: { runtime: "automatic", importSource: "preact" } },
  // The plugin is typed against this package's own vite (the one scripts/build.mjs runs); the config type
  // comes from vite-plus's bundled vite, so the two copies of the Plugin type need this one bridge.
  plugins: [tailwindcss() as PluginOption],
  resolve: {
    alias: [{ find: /^@huddle\/ui\/(.*)$/, replacement: `${shared}/$1` }],
    dedupe: ["preact"],
  },
  // theme.css pulls its IBM Plex faces from the hoisted node_modules at the repository root.
  server: { fs: { allow: [repo] } },
  publicDir: false,
  build: {
    // Outside this workspace, so emptyOutDir must be explicit for a direct `vite build`; scripts/build.mjs
    // writes the files itself and keeps index.html.
    outDir: "../../internal/huddle/web/dist",
    emptyOutDir: true,
    target: "es2022",
    minify: true,
    sourcemap: false,
    modulePreload: false,
    reportCompressedSize: false,
    rolldownOptions: {
      input: "src/main.tsx",
      output: {
        format: "es",
        entryFileNames: "app.[hash].js",
        // the one stylesheet is app.<hash>.css; the bundled fonts collect under fonts/.
        assetFileNames: (asset) =>
          asset.names.some((name) => name.endsWith(".css"))
            ? "app.[hash].css"
            : "fonts/[name].[hash][extname]",
        codeSplitting: false,
      },
    },
  },
  lint: {
    // public/ is the PWA shell the build ships as written and hashes into the service worker's cache
    // revision, so no formatter or linter may touch its bytes; coverage/ is generated.
    ignorePatterns: ["coverage/**", "public/**"],
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
      // Biome gated cognitive complexity at 12; Oxlint's complexity counts cyclomatic paths (every `?.`,
      // `||` and ternary), which reads 13-20 on functions the cognitive gate passed. 20 is the same
      // ceiling in this metric; the handful over it carry a line disable with their own reason.
      { files: ["src/**", "test/**"], rules: { complexity: ["error", { max: 20 }] } },
      // The passkey, clipboard and agent stubs spread the platform object on purpose: vi.stubGlobal
      // replaces the whole navigator, and the code under test reads plain data fields off it.
      { files: ["test/**"], rules: { "typescript/no-misused-spread": "off" } },
    ],
  },
  fmt: {
    ignorePatterns: ["coverage/**", "public/**"],
    useTabs: false,
    tabWidth: 2,
    printWidth: 110,
    singleQuote: false,
  },
  // The owner UI's components, rendered into happy-dom. Shared pieces come from web/ui by alias, on this
  // workspace's own Preact (dedupe), so both dashboards ship one copy of it.
  test: {
    environment: "happy-dom",
    include: ["test/**/*.test.{ts,tsx}"],
    setupFiles: ["test/setup.ts"],
    coverage: {
      provider: "v8",
      include: ["src/**/*.{ts,tsx}"],
      // thin glue around tested logic: the browser entry, and the browser objects it hands radar
      exclude: ["src/main.tsx", "src/radar/browser.ts"],
      thresholds: { lines: 90, branches: 90, functions: 90, statements: 90 },
    },
  },
});
