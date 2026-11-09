import { defineConfig } from "vitest/config";

export default defineConfig({
  build: {
    outDir: "dist",
    sourcemap: true,
  },
  test: {
    // core/ is framework-free and needs no DOM (CLAUDE.md testing conventions).
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
});
