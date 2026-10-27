import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "./client";

/**
 * Readiness of the backend, as the backend reports it. Not a keep-alive: this is what the
 * dashboard shows so "the app is broken" and "the API is down" are distinguishable without
 * opening a terminal.
 */
export function useReadiness() {
  return useQuery({
    queryKey: ["health", "ready"],
    queryFn: async () => unwrap(await api.GET("/health/ready")),
    // A readiness answer is stale almost immediately, and it is cheap.
    staleTime: 10_000,
    refetchInterval: 30_000,
    // One retry, not three: when the API is down, the point is to SAY so, quickly.
    retry: 1,
  });
}
