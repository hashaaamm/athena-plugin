import { useSyncExternalStore } from "react";

import { getToken, subscribe } from "./auth";

/**
 * The access token, as React state. Re-renders every subscriber on sign-in and sign-out.
 *
 * `useSyncExternalStore` rather than a context and a provider: the token is not tree state, it is
 * one module-scoped value that `lib/api/client.ts` also reads from outside React entirely. A
 * context would mean two sources of truth for the same string.
 *
 * It returns the token rather than a `{ isSignedIn }` object on purpose — the snapshot must be
 * referentially stable between renders, and a fresh object every call is an infinite render loop.
 */
export function useAccessToken(): string | null {
  return useSyncExternalStore(subscribe, getToken, getToken);
}
