{% if cookiecutter.use_postgres == "yes" -%}
import { afterEach, describe, expect, it, vi } from "vitest";

import { clearToken, getToken, setToken } from "@/lib/auth";
import { ApiError, SESSION_COOKIE, api, assertOk, authMiddleware, unwrap } from "./client";
{%- else -%}
import { describe, expect, it } from "vitest";

import { ApiError, assertOk, unwrap } from "./client";
{%- endif %}

function result(status: number, body?: { data?: unknown; error?: unknown }) {
  return {
    data: body?.data,
    error: body?.error,
    response: new Response(null, { status }),
  };
}

describe("unwrap", () => {
  it("returns the body on success", () => {
    expect(unwrap(result(200, { data: { count: 1 } }))).toEqual({ count: 1 });
  });

  it("raises the backend's error code, not just its prose", () => {
    const failure = result(404, {
      error: { error: { code: "not_found", message: "No such resource" } },
    });
    expect(() => unwrap(failure)).toThrow(ApiError);
    try {
      unwrap(failure);
    } catch (err) {
      expect(err).toMatchObject({ status: 404, code: "not_found", message: "No such resource" });
    }
  });

  it("recognises FastAPI's own validation shape, which skips the envelope", () => {
    const failure = result(422, { error: { detail: [{ msg: "field required" }] } });
    expect(() => unwrap(failure)).toThrow(
      expect.objectContaining({ code: "validation_error" }),
    );
  });

  it("does not report success when the status is fine but the body is missing", () => {
    // A 200 with no body means the contract changed under us. Returning undefined here would
    // render an empty page and call it a success.
    expect(() => unwrap(result(200))).toThrow(ApiError);
  });
});

describe("assertOk", () => {
  it("accepts a 204, which legitimately has no body", () => {
    expect(() => assertOk(result(204))).not.toThrow();
  });

  it("still throws on a failure", () => {
    expect(() => assertOk(result(500))).toThrow(ApiError);
  });
});
{%- if cookiecutter.use_postgres == "yes" %}

// --- The auth middleware ---------------------------------------------------
//
// Driven directly, with the refresh mocked at `api` and the replay going to a fake `fetch`: the
// behaviour under test is what happens between a 401 arriving and the caller seeing an answer.

const BASE = "http://localhost:8000";
const FRESH = { access_token: "fresh.access.token", token_type: "bearer", expires_in: 900 };

type Params = Parameters<typeof authMiddleware.onResponse>[0];

/** Run a request through `onRequest`, as the client would before sending it. */
function send(path: string, replayFetch: (request: Request) => Promise<Response>, init?: RequestInit) {
  const request = authMiddleware.onRequest({
    request: new Request(`${BASE}${path}`, init),
    schemaPath: path,
    params: {},
    id: "test",
    options: { fetch: replayFetch } as never,
  });
  return { request, schemaPath: path, params: {}, id: "test", options: { fetch: replayFetch } as never };
}

function arrives(sent: ReturnType<typeof send>, status: number): Promise<Response> {
  return authMiddleware.onResponse({ ...sent, response: new Response(null, { status }) } as Params);
}

function refreshAnswers(status: number) {
  return vi.spyOn(api, "POST").mockResolvedValue({
    data: status < 400 ? FRESH : undefined,
    error: status < 400 ? undefined : { error: { code: "unauthorized", message: "No" } },
    response: new Response(null, { status }),
  } as never);
}

/** The replay's fetch: answers with `status`, and records the request it was handed. */
function replayAnswers(status = 200) {
  return vi.fn<(request: Request) => Promise<Response>>(
    async () => new Response("{}", { status }),
  );
}

describe("the auth middleware", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    clearToken();
    window.history.replaceState(null, "", "/");
  });

  it("sends the access token as a bearer header", () => {
    setToken("header.payload.signature");

    const { request } = send("/api/v1/auth/me", replayAnswers());

    expect(request.headers.get("Authorization")).toBe("Bearer header.payload.signature");
  });

  it("answers an expired token with a refresh and a replay, so the caller never sees the 401", async () => {
    setToken("expired.access.token");
    const refresh = refreshAnswers(200);
    const replay = replayAnswers();
    const sent = send("/api/v1/auth/me", replay);

    const answer = await arrives(sent, 401);

    expect(answer.status).toBe(200);
    expect(refresh).toHaveBeenCalledWith("/api/v1/auth/refresh", SESSION_COOKIE);
    expect(replay).toHaveBeenCalledOnce();
    expect(replay.mock.calls[0]?.[0].headers.get("Authorization")).toBe("Bearer fresh.access.token");
    expect(getToken()).toBe("fresh.access.token");
  });

  it("replays a request body intact, because a body can be read only once", async () => {
    setToken("expired.access.token");
    refreshAnswers(200);
    const replay = replayAnswers();
    const body = JSON.stringify({ current_password: "a", new_password: "b" });
    const sent = send("/api/v1/auth/change-password", replay, { method: "POST", body });

    await arrives(sent, 401);

    expect(await replay.mock.calls[0]?.[0].text()).toBe(body);
  });

  it("refreshes once for any number of requests that expire together", async () => {
    // Ten queries on one page all get a 401 at the fifteen-minute mark. Ten refreshes with one
    // single-use cookie is a replay as far as the backend can tell, and it ends the session.
    setToken("expired.access.token");
    const refresh = refreshAnswers(200);
    const replay = replayAnswers();
    const requests = Array.from({ length: 5 }, () => send("/api/v1/auth/me", replay));

    const answers = await Promise.all(requests.map((sent) => arrives(sent, 401)));

    expect(refresh).toHaveBeenCalledOnce();
    expect(replay).toHaveBeenCalledTimes(5);
    expect(answers.map((a) => a.status)).toEqual([200, 200, 200, 200, 200]);
  });

  it("replays without refreshing when another request already has", async () => {
    setToken("expired.access.token");
    const refresh = refreshAnswers(200);
    const replay = replayAnswers();
    const sent = send("/api/v1/auth/me", replay);
    setToken("already.refreshed.token");

    await arrives(sent, 401);

    expect(refresh).not.toHaveBeenCalled();
    expect(replay.mock.calls[0]?.[0].headers.get("Authorization")).toBe(
      "Bearer already.refreshed.token",
    );
  });

  it("ends the session when the refresh is refused", async () => {
    window.history.replaceState(null, "", "/login");
    setToken("expired.access.token");
    refreshAnswers(401);
    const replay = replayAnswers();
    const sent = send("/api/v1/auth/me", replay);

    const answer = await arrives(sent, 401);

    expect(answer.status).toBe(401);
    expect(replay).not.toHaveBeenCalled();
    expect(getToken()).toBeNull();
  });

  it("ends the session when the replay is refused as well", async () => {
    window.history.replaceState(null, "", "/login");
    setToken("expired.access.token");
    refreshAnswers(200);
    const sent = send("/api/v1/auth/me", replayAnswers(401));

    const answer = await arrives(sent, 401);

    expect(answer.status).toBe(401);
    expect(getToken()).toBeNull();
  });

  it("hands a 401 from a session route back to its caller untouched", async () => {
    // A wrong password at sign-in, or no cookie to restore a session from, is an answer.
    setToken("header.payload.signature");
    const refresh = refreshAnswers(200);

    for (const path of ["/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout"]) {
      const answer = await arrives(send(path, replayAnswers()), 401);
      expect(answer.status).toBe(401);
    }

    expect(refresh).not.toHaveBeenCalled();
    expect(getToken()).toBe("header.payload.signature");
  });

  it("does not refresh for a request that carried no token", async () => {
    const refresh = refreshAnswers(200);

    const answer = await arrives(send("/api/v1/auth/me", replayAnswers()), 401);

    expect(answer.status).toBe(401);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("leaves a 403 alone: the session is valid and only the operation was refused", async () => {
    setToken("header.payload.signature");
    const refresh = refreshAnswers(200);

    const answer = await arrives(send("/api/v1/auth/change-password", replayAnswers()), 403);

    expect(answer.status).toBe(403);
    expect(refresh).not.toHaveBeenCalled();
    expect(getToken()).toBe("header.payload.signature");
  });
});
{%- endif %}
