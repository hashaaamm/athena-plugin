// ESLint 9 flat config for the Pulumi program: Node and TypeScript, no browser and no React. The
// same rule sets as the application's config, so a reviewer reads one dialect across the repository.
import js from "@eslint/js";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["node_modules", "*.config.mjs", "*.config.mts"] },
  {
    files: ["**/*.ts"],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
  },
);
