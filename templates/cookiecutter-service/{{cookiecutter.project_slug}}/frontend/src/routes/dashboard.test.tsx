import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "@/lib/api/client";
import { clearToken, setToken } from "@/lib/auth";
import { renderAt } from "@/test-router";

const SHA = "444e2e3be7532f379b84f7d892ce0a7c8995298";

function readyWith(version: string) {
  return vi.spyOn(api, "GET").mockResolvedValue({
    data: { status: "ok", version, database: "ok" },
    error: undefined,
    response: new Response(null, { status: 200 }),
  } as never);
}

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
  window.sessionStorage.clear();
});

describe("the dashboard route", () => {
  // The whole application is behind the guard, this page included. It is the case that gets
  // missed, because `/` is the one URL nobody types to test a redirect.
  it("sends a visitor with no token to sign in, without rendering the page first", async () => {
    const get = vi.spyOn(api, "GET");

    await renderAt("/");

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Dashboard" })).not.toBeInTheDocument();
    // `beforeLoad` ran before the component did, so no query was ever issued.
    expect(get).not.toHaveBeenCalled();
  });

  it("renders for a visitor who holds a token", async () => {
    setToken("header.payload.signature");
    readyWith("test");

    await renderAt("/");

    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
  });

  it("shows a short build identifier rather than the whole commit SHA", async () => {
    setToken("header.payload.signature");
    readyWith(SHA);

    await renderAt("/");

    expect(await screen.findByText("444e2e3")).toBeInTheDocument();
    expect(screen.queryByText(SHA)).not.toBeInTheDocument();
  });
});
