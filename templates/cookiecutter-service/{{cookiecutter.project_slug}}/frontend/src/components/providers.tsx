import { useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";

/**
 * Everything the whole tree needs, in one place, wrapped once in main.tsx.
 *
 * The client is created in `useState` rather than at module scope: a module-level client is
 * shared between every test in a file and between every request if this is ever server-rendered,
 * so one test's cache leaks into the next one's assertions.
 */
export function Providers({ children }: { children: ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // A minute is long enough that navigating back does not refetch, short enough that
            // nobody stares at data from before their own last change.
            staleTime: 60 * 1000,
            refetchOnWindowFocus: false,
          },
        },
      }),
  );

  return (
    <QueryClientProvider client={queryClient}>
      {children}
      <Toaster position="bottom-right" richColors closeButton />
    </QueryClientProvider>
  );
}
