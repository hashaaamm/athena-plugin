// ESLint 9 flat config for the React + Vite + TypeScript SPA.
import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";

export default tseslint.config(
  // Never lint build output or the generated API types — the generator owns that file, and a
  // lint error in it is a rule you cannot fix without editing something a command overwrites.
  { ignores: ["dist", "src/lib/api/schema.d.ts", "*.config.js", "*.config.ts"] },
  {
    files: ["**/*.{ts,tsx}"],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
    },
  },
  {
    // shadcn/ui's generated components export a component AND its cva variants from one file.
    // That is the shape its CLI writes and the shape its docs assume, so the rule loses here
    // rather than every added component needing a hand edit.
    files: ["src/components/ui/**"],
    rules: { "react-refresh/only-export-components": "off" },
  },
);
