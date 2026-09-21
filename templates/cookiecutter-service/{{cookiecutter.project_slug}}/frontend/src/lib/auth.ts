/**
 * The access token: where it lives, who may read it, and what happens when the server rejects it.
 *
 * No React in this file. It is imported by `lib/api/client.ts`, which must not depend on a
 * component tree — `use-session.ts` is the hook that makes it reactive.
 *
 * ## Where the token lives, and what that costs
 *
 * `sessionStorage`, under a project-scoped key. The decision and the trade-off are written up in
 * `AGENTS.md`; the short version is the part you need before editing this file:
 *
 * * It survives a page refresh. An in-memory-only token does not, and with no refresh endpoint
 *   in this backend that means F5 signs the user out.
 * * It does **not** survive closing the tab, and a second tab starts signed out. That is the
 *   price of the line above and it is deliberate.
 * * It is **not** protected from XSS. Any script running in this page can read it, exactly as it
 *   could read `localStorage`. Choosing `sessionStorage` bought a smaller window, not safety. A
 *   product holding data worth stealing wants an httpOnly cookie and a refresh endpoint, which
 *   is backend work this template has not done — see `AGENTS.md`.
 */

//: Namespaced by project, so two of these apps served from the same origin during development do
//: not read each other's sessions.
const STORAGE_KEY = "{{ cookiecutter.project_slug }}.access_token";

/** Where an unauthenticated or de-authenticated visitor lands. One constant, used by both. */
export const LOGIN_PATH = "/login";

/**
 * The token, cached in module scope so a read is a variable access rather than a storage hit on
 * every request. `sessionStorage` is the durable copy; this is the one that answers `getToken`.
 */
let token: string | null = readStored();

const listeners = new Set<() => void>();

function readStored(): string | null {
  try {
    return window.sessionStorage.getItem(STORAGE_KEY);
  } catch {
    // Storage can be unavailable — Safari's private mode historically, an embedded webview, a
    // blocked-cookies setting. The app still works; the session just ends with the page.
    return null;
  }
}

function writeStored(next: string | null): void {
  try {
    if (next === null) window.sessionStorage.removeItem(STORAGE_KEY);
    else window.sessionStorage.setItem(STORAGE_KEY, next);
  } catch {
    // Same as above: in-memory only, rather than a crash on sign-in.
  }
}

function publish(): void {
  for (const listener of listeners) listener();
}

/** The current access token, or `null`. Synchronous — every caller here is. */
export function getToken(): string | null {
  return token;
}

/** Record a freshly minted token. Called by `useSignIn`, and nowhere else worth having. */
export function setToken(next: string): void {
  token = next;
  writeStored(next);
  publish();
}

/** Forget the session. Idempotent, because both sign-out and a 401 call it. */
export function clearToken(): void {
  if (token === null) return;
  token = null;
  writeStored(null);
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
 * A full document load rather than a router navigation, and that is the point: the TanStack Query
 * cache holds whatever the ended session read, and a soft navigate would leave it in memory for
 * the next person at the keyboard.
 */
function hardRedirect(path: string): void {
  window.location.assign(path);
}

/**
 * The single answer to "the server said 401". Registered once as response middleware in
 * `lib/api/client.ts`; no hook, page or component handles a 401 itself.
 *
 * Two guards, and both have bitten people who left them out:
 *
 * * `getToken() === null` — a 401 only means "the session is over" if we believed we had one.
 *   Without this, a wrong password on the login form logs an anonymous visitor out of nothing
 *   and navigates away from the error they were supposed to read.
 * * The pathname check — redirecting to `/login` from `/login` is how you write a reload loop.
 *
 * Note what is **not** here: 403. The backend answers a wrong current password on
 * `/auth/change-password` with 403 precisely so this function ignores it; the session is fine and
 * the form shows the error. Ask Athena for the JWT authentication guide.
 *
 * `redirect` is a parameter so the behaviour is unit-testable without stubbing `window.location`.
 */
export function enforceSession(
  response: Response,
  redirect: (path: string) => void = hardRedirect,
): void {
  if (response.status !== 401 || token === null) return;
  clearToken();
  if (window.location.pathname !== LOGIN_PATH) redirect(LOGIN_PATH);
}
