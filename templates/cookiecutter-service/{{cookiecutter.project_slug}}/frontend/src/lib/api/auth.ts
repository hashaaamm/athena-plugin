import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { clearToken, setToken } from "@/lib/auth";
import type { components } from "./schema";
import { api, assertOk, unwrap } from "./client";

export type User = components["schemas"]["UserRead"];
export type Credentials = components["schemas"]["LoginRequest"];
export type PasswordChange = components["schemas"]["ChangePasswordRequest"];

/**
 * Query keys in one object. `all` is the prefix everything else nests under, so signing out can
 * drop the whole subtree without naming each key.
 */
export const authKeys = {
  all: ["auth"] as const,
  me: () => ["auth", "me"] as const,
};

// --- Fetchers ------------------------------------------------------------
//
// Plain async functions. Testing one needs no React, no provider and no query client, which is
// why these are the functions with tests.

/** Create an account. Returns the user and **no** session — the backend does not log you in. */
export async function registerAccount(body: Credentials): Promise<User> {
  return unwrap(await api.POST("/api/v1/auth/register", { body }));
}

/** Exchange credentials for an access token. Does not store it; `useSignIn` does that. */
export async function requestToken(body: Credentials) {
  return unwrap(await api.POST("/api/v1/auth/login", { body }));
}

export async function fetchMe(): Promise<User> {
  return unwrap(await api.GET("/api/v1/auth/me"));
}

/** 204 on success, so there is no body to unwrap — only a status to insist on. */
export async function changePassword(body: PasswordChange): Promise<void> {
  assertOk(await api.POST("/api/v1/auth/change-password", { body }));
}

// --- Hooks ---------------------------------------------------------------

/**
 * The signed-in user, read from the server rather than decoded from the token.
 *
 * `enabled` keeps it from firing an anonymous request that would 401 for no reason, and `retry`
 * is off because a 401 or a 403 fails identically three times — see the handbook's rule about
 * never retrying a 4xx.
 */
export function useMe(token: string | null) {
  return useQuery({
    queryKey: authKeys.me(),
    queryFn: fetchMe,
    enabled: token !== null,
    retry: false,
  });
}

/**
 * Sign in: mint a token, then store it. Storing it inside the mutation rather than at the call
 * site means every caller gets the same behaviour, including the one written next year.
 */
export function useSignIn() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (credentials: Credentials) => {
      const token = await requestToken(credentials);
      setToken(token.access_token);
      return token;
    },
    // A previous session's `me` may still be cached under the same key.
    onSuccess: () => queryClient.invalidateQueries({ queryKey: authKeys.all }),
  });
}

export function useRegister() {
  return useMutation({ mutationFn: registerAccount });
}

/**
 * Change the password. The caller's own token keeps working — this backend issues no refresh
 * tokens and stores no sessions, so there is nothing to revoke and nothing to re-request.
 */
export function useChangePassword() {
  return useMutation({ mutationFn: changePassword });
}

/**
 * Sign out. Clearing the token is half of it; emptying the cache is the other half, or the next
 * person at this keyboard sees the last one's data until it goes stale.
 */
export function useSignOut() {
  const queryClient = useQueryClient();
  return () => {
    clearToken();
    queryClient.clear();
  };
}
