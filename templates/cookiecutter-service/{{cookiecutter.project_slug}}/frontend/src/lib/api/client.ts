import createClient{% if cookiecutter.use_postgres == "yes" %}, { type Middleware }{% endif %} from "openapi-fetch";

{% if cookiecutter.use_postgres == "yes" -%}
import { endSession, enforceSession, getToken, refreshOnce } from "@/lib/auth";
{% endif -%}
import type { paths } from "./schema";

/**
 * The one HTTP client in the app, typed by the backend's own OpenAPI document.
 *
 * Nothing else in this codebase calls `fetch` against our API. That is not style: every request
 * made anywhere else is a request that skips the base URL, the error contract and the types.
 *
 * `import.meta.env.VITE_API_URL` is inlined at BUILD time, so a deployed bundle cannot be
 * repointed by changing an environment variable on the container.
 */
export const api = createClient<paths>({
  baseUrl: import.meta.env.VITE_API_URL ?? "http://localhost:8000",
});

{% if cookiecutter.use_postgres == "yes" -%}
/**
 * The options every call that carries the refresh cookie passes: the cookie itself, and the header
 * the backend's CSRF check requires on refresh and logout. A cross-site page cannot add a custom
 * header without a CORS preflight the backend's origin allowlist refuses.
 *
 * `credentials: "include"` goes on these calls only, not on the client. Every other request is a
 * bearer-token request with no cookie in play, and enabling credentials there buys a stricter CORS
 * contract and nothing else. Login and change-password use `WITH_COOKIE` alone: they do not read the
 * cookie, but a browser ignores the `Set-Cookie` of a cross-origin response without it.
 */
export const WITH_COOKIE = { credentials: "include" } as const;
export const SESSION_COOKIE = {
  credentials: "include",
  headers: { "X-Requested-With": "fetch" },
} as const;

/**
 * The routes that answer for the refresh cookie rather than for a bearer token. A 401 from one of
 * them is an answer — wrong password, no session to restore — and never a reason to refresh.
 */
const SESSION_ROUTES = new Set(["/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout"]);

/** Spend the refresh cookie. Resolves to the new access token, or `null` if the backend refused. */
async function requestRefresh(): Promise<string | null> {
  const { data, response } = await api.POST("/api/v1/auth/refresh", SESSION_COOKIE);
  return response.ok && data !== undefined ? data.access_token : null;
}

/**
 * A new access token from the refresh cookie, shared by every concurrent caller — see `refreshOnce`
 * in `lib/auth.ts` for why one at a time is a correctness rule rather than an optimisation. The
 * route guard calls it after a reload; the middleware below calls it on a 401.
 */
export function refreshSession(): Promise<string | null> {
  return refreshOnce(requestRefresh);
}

/**
 * An unread copy of every request sent with a bearer token, kept until its response arrives. A
 * request body can be read once, and replaying a POST after a refresh needs one nobody has read.
 */
const replays = new WeakMap<Request, Request>();

/**
 * Authentication, attached once. Both halves are middleware because both halves are properties
 * of *every* request, and a property enforced at call sites is a property until somebody forgets.
 *
 * `onRequest` sends the bearer token when there is one. `onResponse` is the only code in this
 * application that decides what a 401 means. For a request that carried an access token it is
 * almost always "that token expired", so it refreshes — once, however many requests failed
 * together — and replays the request with the new token; the caller sees the replay's answer and
 * never the 401. Only when the refresh is refused, or the replay is refused too, is the session
 * over. Nothing else, anywhere, handles a 401.
 */
export const authMiddleware = {
  onRequest({ request, schemaPath }) {
    const token = getToken();
    if (token === null) return request;
    request.headers.set("Authorization", `Bearer ${token}`);
    if (!SESSION_ROUTES.has(schemaPath)) replays.set(request, request.clone());
    return request;
  },
  async onResponse({ request, response, schemaPath, options }) {
    // Whoever called a session route reads its answer: the sign-in form, `refreshSession`, sign-out.
    if (SESSION_ROUTES.has(schemaPath)) return response;
    const replay = replays.get(request);
    replays.delete(request);
    if (response.status !== 401 || replay === undefined) {
      enforceSession(response);
      return response;
    }
    // Another request may have refreshed while this one was in flight. Its token is the one to
    // replay with; spending the cookie again would only rotate it for nothing.
    const current = getToken();
    const sent = request.headers.get("Authorization");
    const token =
      current !== null && sent !== `Bearer ${current}` ? current : await refreshSession();
    if (token === null) {
      endSession();
      return response;
    }
    replay.headers.set("Authorization", `Bearer ${token}`);
    const replayed = await options.fetch(replay);
    enforceSession(replayed);
    return replayed;
  },
} satisfies Middleware;

api.use(authMiddleware);
{% else -%}
// Where a middleware goes when you need one — a bearer token, a correlation id, a 401 redirect:
//
//   api.use({
//     onRequest({ request }) {
//       request.headers.set("Authorization", `Bearer ${token()}`);
//       return request;
//     },
//   });
//
// Register it here, once, rather than at a call site. Ask Athena for the frontend auth rules
// before you write the token half.
{% endif %}

/** A failed API call, carrying the backend's stable error code rather than only its prose. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

/**
 * Read the backend's error envelope. The API answers every failure with
 * `{ error: { code, message } }` — one shape, so there is one reader for it. FastAPI's own
 * validation errors (`detail`) bypass that envelope, which is why they are handled here too.
 */
function describeError(status: number, body: unknown): ApiError {
  if (typeof body === "object" && body !== null) {
    const envelope = (body as { error?: { code?: unknown; message?: unknown } }).error;
    if (envelope && typeof envelope.code === "string") {
      const message =
        typeof envelope.message === "string" ? envelope.message : envelope.code;
      return new ApiError(status, envelope.code, message);
    }
    if ("detail" in body) return new ApiError(status, "validation_error", "Invalid request");
  }
  // No envelope at all: the network failed, a proxy answered, or the request never arrived.
  return new ApiError(status, status === 0 ? "network_error" : "unexpected_error", "Request failed");
}

/** What openapi-fetch hands back. Declared structurally so the helpers stay method-agnostic. */
type Result<T> = { data?: T; error?: unknown; response: Response };

/**
 * Return the response body, or throw an `ApiError`. Every query and mutation goes through this,
 * so a failure surfaces as a thrown error TanStack Query can see — returning `undefined` on a
 * 500 would render an empty list and call it success.
 */
export function unwrap<T>(result: Result<T>): T {
  if (!result.response.ok || result.data === undefined) {
    throw describeError(result.response.status, result.error);
  }
  return result.data;
}

/** The same check for endpoints that answer 204 and have no body to return. */
export function assertOk(result: Result<unknown>): void {
  if (!result.response.ok) throw describeError(result.response.status, result.error);
}
