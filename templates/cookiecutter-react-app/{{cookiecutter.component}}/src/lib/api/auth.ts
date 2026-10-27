import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { clearToken, setToken } from "@/lib/auth";
import type { components } from "./schema";
import { SESSION_COOKIE, WITH_COOKIE, api, assertOk, unwrap } from "./client";

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

/**
 * Exchange credentials for an access token. The refresh token arrives as an httpOnly cookie the
 * browser keeps and this code never sees. Does not store the access token; `useSignIn` does that.
 */
export async function requestToken(body: Credentials) {
  return unwrap(await api.POST("/api/v1/auth/login", { body, ...WITH_COOKIE }));
}

export async function fetchMe(): Promise<User> {
  return unwrap(await api.GET("/api/v1/auth/me"));
}

/**
 * Replace the password. The backend ends every session the account has and answers with a new
 * one for this browser: an access token in the body and a refresh cookie beside it.
 */
export async function changePassword(body: PasswordChange) {
  return unwrap(await api.POST("/api/v1/auth/change-password", { body, ...WITH_COOKIE }));
}

/** End this browser's session on the server, and have it clear the refresh cookie. Always 204. */
export async function endServerSession(): Promise<void> {
  assertOk(await api.POST("/api/v1/auth/logout", SESSION_COOKIE));
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
 * Change the password. Every other device is signed out by it; this one is handed a new session,
 * so the new access token is stored here, inside the mutation, where no caller can forget it.
 */
export function useChangePassword() {
  return useMutation({
    mutationFn: async (body: PasswordChange) => {
      const token = await changePassword(body);
      setToken(token.access_token);
      return token;
    },
  });
}

/**
 * Sign out: end the session on the server, then here. Three parts, and each one matters.
 *
 * The server call is what makes signing out mean something — without it the refresh cookie would
 * still mint access tokens for anyone who opened the app again. It is best-effort: a network
 * failure must not leave the user looking at a session they asked to end, so the local half runs
 * whatever happens. Clearing the token is the second part, and emptying the cache the third, or the
 * next person at this keyboard sees the last one's data until it goes stale.
 */
export function useSignOut() {
  const queryClient = useQueryClient();
  return async () => {
    try {
      await endServerSession();
    } catch {
      // Unreachable server: the cookie outlives this, but nothing here can reach it either.
    }
    clearToken();
    queryClient.clear();
  };
}
