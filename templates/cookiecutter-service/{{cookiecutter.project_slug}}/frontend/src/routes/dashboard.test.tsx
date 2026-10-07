import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SESSION_COOKIE, api } from "@/lib/api/client";
import { clearToken, getToken, setToken } from "@/lib/auth";
import { renderAt } from "@/test-router";

const SHA = "444e2e3be7532f379b84f7d892ce0a7c8995298";

function readyWith(version: string) {
  return vi.spyOn(api, "GET").mockResolvedValue({
    data: { status: "ok", version, database: "ok" },
    error: undefined,
    response: new Response(null, { status: 200 }),
  } as never);
}

/** What `POST /auth/refresh` answers: a new access token, or a 401 when there is no session. */
function refreshAnswers(status: number) {
  return vi.spyOn(api, "POST").mockResolvedValue({
    data:
      status < 400
        ? { access_token: "restored.access.token", token_type: "bearer", expires_in: 900 }
        : undefined,
    error: status < 400 ? undefined : { error: { code: "unauthorized", message: "No session" } },
    response: new Response(null, { status }),
  } as never);
}

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
});

describe("the dashboard route", () => {
  // The whole application is behind the guard, this page included. It is the case that gets
  // missed, because `/` is the one URL nobody types to test a redirect.
  it("sends a visitor with no session to sign in, without rendering the page first", async () => {
    const get = vi.spyOn(api, "GET");
    const refresh = refreshAnswers(401);

    await renderAt("/");

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Dashboard" })).not.toBeInTheDocument();
    // The guard asked the refresh cookie before giving up on the visitor…
    expect(refresh).toHaveBeenCalledWith("/api/v1/auth/refresh", SESSION_COOKIE);
    // …and `beforeLoad` ran before the component did, so no query was ever issued.
    expect(get).not.toHaveBeenCalled();
  });

  it("restores the session from the refresh cookie after a reload", async () => {
    // The access token lives in memory, so a reload or a new tab has none. That is not the
    // same as being signed out, and the visitor must not be sent to sign in for it.
    readyWith("test");
    refreshAnswers(200);

    await renderAt("/");

    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
    expect(getToken()).toBe("restored.access.token");
  });

  it("renders for a visitor who holds a token, without spending the cookie", async () => {
    setToken("header.payload.signature");
    readyWith("test");
    const refresh = refreshAnswers(200);

    await renderAt("/");

    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
    expect(refresh).not.toHaveBeenCalled();
  });

  it("shows a short build identifier rather than the whole commit SHA", async () => {
    setToken("header.payload.signature");
    readyWith(SHA);

    await renderAt("/");

    expect(await screen.findByText("444e2e3")).toBeInTheDocument();
    expect(screen.queryByText(SHA)).not.toBeInTheDocument();
  });
});

describe("signing out", () => {
  it("ends the session on the server, forgets the token, and lands on sign in", async () => {
    setToken("header.payload.signature");
    readyWith("test");
    const post = vi.spyOn(api, "POST").mockResolvedValue({
      data: undefined,
      error: undefined,
      response: new Response(null, { status: 204 }),
    } as never);
    await renderAt("/");
    await screen.findByRole("heading", { name: "Dashboard" });

    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(post).toHaveBeenCalledWith("/api/v1/auth/logout", SESSION_COOKIE);
    expect(getToken()).toBeNull();
  });

  it("still signs out here when the server cannot be reached", async () => {
    setToken("header.payload.signature");
    readyWith("test");
    vi.spyOn(api, "POST").mockRejectedValue(new TypeError("Failed to fetch"));
    await renderAt("/");
    await screen.findByRole("heading", { name: "Dashboard" });

    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(getToken()).toBeNull();
  });
});
