/**
 * The access token: where it lives, who may read it, and what happens when the server rejects it.
 *
 * No React in this file, and no API client. It is imported by `lib/api/client.ts`, which must not
 * depend on a component tree — `use-session.ts` is the hook that makes it reactive.
 *
 * ## Where the credentials live, and what that costs
 *
 * Two credentials, two places. The decision and its trade-offs are written up in `AGENTS.md`; the
 * short version is the part you need before editing this file:
 *
 * * The **refresh token** is an httpOnly cookie the backend sets. No script in this page can read
 *   it — not this module, not an injected one — so it cannot be carried off and replayed from
 *   somewhere else. The browser attaches it to the three auth calls that ask for it.
 * * The **access token** lives here, in a module variable, and nowhere else: not `sessionStorage`,
 *   not `localStorage`. A reload loses it, and the guard in `router.tsx` gets a new one from the
 *   cookie before any page renders. It lasts fifteen minutes; a 401 from an expired one is
 *   answered by a refresh and a replay in the client middleware, not by a sign-in screen.
 * * Neither is protection from XSS. Script running in this page can still *use* the session while
 *   the page is open — call the API, read this variable. What the cookie removes is the theft of a
 *   long-lived credential, not the attack.
 */

/** Where an unauthenticated or de-authenticated visitor lands. One constant, used by both. */
export const LOGIN_PATH = "/login";

/**
 * The name the Web Locks API serialises refreshes under, namespaced by project. Shared by every tab
 * of this app, which is the point — see `refreshOnce`.
 */
const REFRESH_LOCK = "{{ cookiecutter.project_slug }}.refresh";

/** The access token, or `null`. In memory only, so it dies with the page. */
let token: string | null = null;

/** The refresh in flight, if there is one. Every caller that needs a token waits on this one. */
let inflight: Promise<string | null> | null = null;

const listeners = new Set<() => void>();

function publish(): void {
  for (const listener of listeners) listener();
}

/** The current access token, or `null`. Synchronous — every caller here is. */
export function getToken(): string | null {
  return token;
}

/** Record a freshly minted access token. Called after sign-in, refresh and a password change. */
export function setToken(next: string): void {
  token = next;
  publish();
}

/** Forget the access token. Idempotent, because sign-out, a 401 and a failed refresh all call it. */
export function clearToken(): void {
  if (token === null) return;
  token = null;
  publish();
}

/**
 * Subscribe to sign-in and sign-out. Returns the unsubscribe function, which is the contract
 * `useSyncExternalStore` expects.
 */
export function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * Run `work` with this app's refresh lock held, in every tab at once. The refresh cookie is shared
 * by all of them, and two tabs spending it together look exactly like a replay to the backend,
 * which revokes the whole session. Serialised, the second tab simply spends the cookie the first
 * one was handed. Where the API is missing — old browsers, jsdom — it runs unserialised.
 */
async function exclusively(work: () => Promise<string | null>): Promise<string | null> {
  if (typeof navigator !== "undefined" && "locks" in navigator) {
    return await navigator.locks.request(REFRESH_LOCK, work);
  }
  return await work();
}

/**
 * Get a new access token from the refresh cookie, with at most one refresh in flight.
 *
 * Every concurrent caller — ten queries that all got a 401, the route guard after a reload — shares
 * the one promise. That is not an optimisation. A refresh token is single-use: a second refresh
 * sent with the same cookie is a replay, and the backend answers a replay by revoking every token
 * in the session. A client that refreshes in parallel signs its own user out.
 *
 * `refresh` does the request and returns the new token, or `null` when the backend refused. A
 * thrown error counts as a refusal. On success the token is stored before any waiter resumes.
 */
export function refreshOnce(refresh: () => Promise<string | null>): Promise<string | null> {
  inflight ??= exclusively(refresh)
    .catch(() => null)
    .then((next) => {
      if (next !== null) setToken(next);
      return next;
    })
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

/**
 * A full document load rather than a router navigation, and that is the point: the TanStack Query
 * cache holds whatever the ended session read, and a soft navigate would leave it in memory for
 * the next person at the keyboard.
 */
function hardRedirect(path: string): void {
  window.location.assign(path);
}

/**
 * The session is over: forget the token and go to sign in. The pathname check is the guard against
 * a reload loop — redirecting to `/login` from `/login` requests again, fails again, and reloads.
 *
 * `redirect` is a parameter so the behaviour is unit-testable without stubbing `window.location`.
 */
export function endSession(redirect: (path: string) => void = hardRedirect): void {
  clearToken();
  if (window.location.pathname !== LOGIN_PATH) redirect(LOGIN_PATH);
}

/**
 * The final answer to "the server said 401", once the client middleware has already tried a
 * refresh and a replay, or had no session to refresh. No hook, page or component handles a 401.
 *
 * `getToken() === null` is the guard that matters: a 401 only means "the session is over" if we
 * believed we had one. Without it, a wrong password on the login form logs an anonymous visitor
 * out of nothing and navigates away from the error they were supposed to read.
 *
 * Note what is **not** here: 403. The backend answers a wrong current password on
 * `/auth/change-password` with 403 precisely so this function ignores it; the session is fine and
 * the form shows the error.
 */
export function enforceSession(
  response: Response,
  redirect: (path: string) => void = hardRedirect,
): void {
  if (response.status !== 401 || token === null) return;
  endSession(redirect);
}
