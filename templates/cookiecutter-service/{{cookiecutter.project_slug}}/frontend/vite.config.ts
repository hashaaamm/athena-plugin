import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Tailwind v4 is a Vite plugin, not a PostCSS step: there is no tailwind.config.js and no
// postcss.config.js in this project by design. The theme lives in src/index.css.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") }, // shadcn/ui generates imports as "@/..."
  },
  server: { port: 3000, host: true },
  preview: { port: 8080, host: true },
});
