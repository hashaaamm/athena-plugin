import { RouterProvider } from "@tanstack/react-router";

import { router } from "@/router";

/**
 * Root component: hands the typed route tree to TanStack Router.
 * Providers (Query, toasts) wrap this in main.tsx, because a provider that lives inside the
 * router cannot be read by a route loader.
 */
export function App() {
  return <RouterProvider router={router} />;
}
