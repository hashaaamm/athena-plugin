import path from "node:path";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Separate from vite.config.ts on purpose: the test run needs neither Tailwind nor the dev
// server, and loading them makes every `pnpm test` pay for a CSS pipeline it never reads.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  test: {
    // The application's tests, and only those. infra/pulumi has its own vitest suite, run against
    // Pulumi's mocks in Node, and it would fail here under jsdom without its own dependencies.
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
