import "@fontsource/inter/400.css";
import "@fontsource/inter/500.css";
import "@fontsource/inter/600.css";
import "@fontsource/inter/700.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { Providers } from "@/components/providers";
import { App } from "@/App";
import "@/index.css";

// Self-hosted weights rather than a Google Fonts <link>: no third-party request on first paint,
// no layout shift when it resolves, and no CDN in the privacy story.

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("Missing #root element in index.html");

createRoot(rootEl).render(
  <StrictMode>
    <Providers>
      <App />
    </Providers>
  </StrictMode>,
);
