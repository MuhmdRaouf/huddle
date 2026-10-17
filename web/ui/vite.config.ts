import { defineConfig } from "vite-plus";

export default defineConfig({
  oxc: { jsx: { runtime: "automatic", importSource: "preact" } },
  lint: {
    ignorePatterns: ["coverage/**"],
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
    ignorePatterns: ["coverage/**"],
    tabWidth: 2,
    useTabs: false,
    printWidth: 110,
    singleQuote: false,
  },
  test: {
    environment: "happy-dom",
    include: ["test/**/*.test.{ts,tsx}"],
    setupFiles: ["test/setup.ts"],
    coverage: {
      provider: "v8",
      include: ["src/**/*.{ts,tsx}"],
      thresholds: { lines: 95, branches: 95, functions: 95, statements: 95 },
    },
  },
});
