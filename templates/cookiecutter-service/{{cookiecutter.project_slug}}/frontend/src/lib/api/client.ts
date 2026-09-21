import createClient from "openapi-fetch";

{% if cookiecutter.use_postgres == "yes" -%}
import { enforceSession, getToken } from "@/lib/auth";
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
 * Authentication, attached once. Both halves are middleware because both halves are properties
 * of *every* request, and a property enforced at call sites is a property until somebody forgets.
 *
 * `onRequest` sends the bearer token when there is one. `onResponse` hands every answer to
 * `enforceSession`, which is the only code in this application that decides what a 401 means —
 * see `lib/auth.ts` for the guards that keep it from firing on a failed login or looping on
 * `/login`. Nothing else, anywhere, handles a 401.
 */
api.use({
  onRequest({ request }) {
    const token = getToken();
    if (token !== null) request.headers.set("Authorization", `Bearer ${token}`);
    return request;
  },
  onResponse({ response }) {
    enforceSession(response);
    return response;
  },
});
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
