import { defineConfig } from "vite-plus";

// The board page builds into internal/kanban/web/dist, where the kanban binary embeds it
// next to the server that serves it. The entry is the index.html at this project's root; its module graph
// pulls in src/main.tsx and src/styles.css. Asset URLs are relative so the export can inline them.
export default defineConfig({
  oxc: { jsx: { runtime: "automatic", importSource: "preact" } },
  base: "./",
  build: {
    // Outside this workspace, so emptyOutDir must be explicit for a direct `vite build`; scripts/build.mjs
    // clears the folder itself and writes the files there.
    outDir: "../../internal/kanban/web/dist",
    emptyOutDir: true,
    target: "es2022",
    minify: true,
    sourcemap: false,
    modulePreload: false,
    reportCompressedSize: false,
  },
  resolve: { dedupe: ["preact"] },
  lint: {
    // the fixtures stand in for board state exactly as a ticket writes it
    ignorePatterns: ["test/fixtures/**", "coverage/**"],
    options: { typeAware: true, typeCheck: true },
    rules: {
      "typescript/no-explicit-any": "error",
      "import/no-default-export": "error",
      "typescript/consistent-type-imports": "error",
      "typescript/no-non-null-assertion": "error",
      complexity: ["error", { max: 12 }],
    },
    overrides: [{ files: ["vite.config.ts"], rules: { "import/no-default-export": "off" } }],
  },
  fmt: {
    ignorePatterns: ["test/fixtures/**", "coverage/**"],
    tabWidth: 2,
    useTabs: false,
    printWidth: 110,
    singleQuote: false,
  },
  test: {
    testTimeout: 30_000,
    projects: [
      // the store and the pure data functions: plain Node
      { extends: true, test: { name: "node", include: ["test/**/*.test.ts"], environment: "node" } },
      // the Preact components, rendered into happy-dom
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
