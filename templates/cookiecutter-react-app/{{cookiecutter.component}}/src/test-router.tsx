import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { act, render } from "@testing-library/react";

import { createAppRouter } from "@/router";

/**
 * Mount the real route tree at a given URL, over an in-memory history.
 *
 * Route guards, redirects and "where does a failed sign-in leave you" are properties of the
 * router, not of a component, and a test that renders a page directly cannot see any of them.
 * Retries are off: with them on, a test for a 401 waits out three backoffs before it can assert.
 */
export async function renderAt(path: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));

  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  }

  const result = render(<RouterProvider router={router} />, { wrapper: Wrapper });
  // The router resolves its first match asynchronously — `beforeLoad` guards included — so
  // without this every query in the test races an empty document and fails on the happy path.
  await act(async () => {
    await router.load();
  });
  return { ...result, router };
}
