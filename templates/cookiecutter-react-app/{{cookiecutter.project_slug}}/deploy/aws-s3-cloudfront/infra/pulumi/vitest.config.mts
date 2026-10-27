// .mts, because vitest's config loader is ESM and this package is CommonJS — Pulumi runs it that way.
import { defineConfig } from "vitest/config";

// The stack's tests run in Node against Pulumi's mocks — no credentials, no network.
export default defineConfig({
  test: {
    include: ["tests/**/*.test.ts"],
    environment: "node",
    // A process per test file. Pulumi's runtime keeps its settings and its mock engine in
    // process-wide state, so two files sharing a worker thread would share one engine.
    pool: "forks",
  },
});
