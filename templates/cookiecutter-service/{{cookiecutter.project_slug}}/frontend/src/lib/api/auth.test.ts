import { afterEach, describe, expect, it, vi } from "vitest";

import { clearToken, getToken } from "@/lib/auth";
import { ApiError, SESSION_COOKIE, api } from "./client";
import {
  changePassword,
  endServerSession,
  fetchMe,
  registerAccount,
  requestToken,
} from "./auth";

const CREDENTIALS = { email: "ada@example.com", password: "correct-horse-battery" };

const USER = {
  id: "0f4f2b7e-6a4a-4f2e-9a3f-1c2d3e4f5a6b",
  email: "ada@example.com",
  is_active: true,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

function ok(status: number, data?: unknown) {
  return { data, error: undefined, response: new Response(null, { status }) };
}

function failure(status: number, code: string, message: string) {
  return {
    data: undefined,
    error: { error: { code, message } },
    response: new Response(null, { status }),
  };
}

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
});

describe("registerAccount", () => {
  it("posts the credentials and returns the created user", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok(201, USER) as never);

    await expect(registerAccount(CREDENTIALS)).resolves.toEqual(USER);
    expect(post).toHaveBeenCalledWith("/api/v1/auth/register", { body: CREDENTIALS });
  });

  it("surfaces the backend's conflict code, which registration deliberately discloses", async () => {
    vi.spyOn(api, "POST").mockResolvedValue(
      failure(409, "conflict", "An account with that email already exists") as never,
    );

    await expect(registerAccount(CREDENTIALS)).rejects.toMatchObject({
      status: 409,
      code: "conflict",
    });
  });
});

describe("requestToken", () => {
  it("returns the token without storing it — that is the hook's job, not the fetcher's", async () => {
    vi.spyOn(api, "POST").mockResolvedValue(
      ok(200, { access_token: "a.b.c", token_type: "bearer", expires_in: 900 }) as never,
    );

    const token = await requestToken(CREDENTIALS);

    expect(token.access_token).toBe("a.b.c");
    expect(getToken()).toBeNull();
  });

  it("lets the browser keep the refresh cookie the answer sets", async () => {
    // Cross-origin, a browser ignores a response's Set-Cookie unless the request asked for
    // credentials. Without this the sign-in works and the first reload signs the user out.
    const post = vi.spyOn(api, "POST").mockResolvedValue(
      ok(200, { access_token: "a.b.c", token_type: "bearer", expires_in: 900 }) as never,
    );

    await requestToken(CREDENTIALS);

    expect(post).toHaveBeenCalledWith("/api/v1/auth/login", {
      body: CREDENTIALS,
      credentials: "include",
    });
  });

  it("raises the 401 the backend answers to every bad credential alike", async () => {
    vi.spyOn(api, "POST").mockResolvedValue(
      failure(401, "unauthorized", "Invalid email or password") as never,
    );

    await expect(requestToken(CREDENTIALS)).rejects.toBeInstanceOf(ApiError);
  });
});

describe("fetchMe", () => {
  it("reads the user from the server rather than from the token", async () => {
    const get = vi.spyOn(api, "GET").mockResolvedValue(ok(200, USER) as never);

    await expect(fetchMe()).resolves.toEqual(USER);
    expect(get).toHaveBeenCalledWith("/api/v1/auth/me");
  });
});

describe("changePassword", () => {
  const BODY = { current_password: "old-password-here", new_password: "new-password-here" };

  it("returns the new session the backend starts, with the cookie it sets kept", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(
      ok(200, { access_token: "new.access.token", token_type: "bearer", expires_in: 900 }) as never,
    );

    await expect(changePassword(BODY)).resolves.toMatchObject({ access_token: "new.access.token" });
    expect(post).toHaveBeenCalledWith("/api/v1/auth/change-password", {
      body: BODY,
      credentials: "include",
    });
  });

  it("carries the 403 through as a 403, so the form can show it on the field", async () => {
    vi.spyOn(api, "POST").mockResolvedValue(
      failure(403, "forbidden", "Current password is incorrect") as never,
    );

    await expect(changePassword(BODY)).rejects.toMatchObject({
      status: 403,
      code: "forbidden",
      message: "Current password is incorrect",
    });
  });
});

describe("endServerSession", () => {
  it("sends the refresh cookie and the CSRF header, and accepts the 204", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok(204) as never);

    await expect(endServerSession()).resolves.toBeUndefined();
    expect(post).toHaveBeenCalledWith("/api/v1/auth/logout", SESSION_COOKIE);
    expect(SESSION_COOKIE).toMatchObject({
      credentials: "include",
      headers: { "X-Requested-With": "fetch" },
    });
  });
});
