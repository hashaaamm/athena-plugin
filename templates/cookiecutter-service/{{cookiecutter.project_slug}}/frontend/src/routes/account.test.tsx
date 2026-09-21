import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "@/lib/api/client";
import { clearToken, getToken, setToken } from "@/lib/auth";
import { renderAt } from "@/test-router";

const EMAIL = "ada@example.com";
const CURRENT = "correct-horse-battery";
const NEXT = "another-long-passphrase";

const USER = {
  id: "0f4f2b7e-6a4a-4f2e-9a3f-1c2d3e4f5a6b",
  email: EMAIL,
  is_active: true,
  created_at: "2026-01-02T03:04:05Z",
  updated_at: "2026-01-02T03:04:05Z",
};

function answer(status: number, body?: unknown) {
  return {
    data: status < 400 ? body : undefined,
    error: status < 400 ? undefined : body,
    response: new Response(null, { status }),
  };
}

function meReturns(status: number, body?: unknown) {
  return vi.spyOn(api, "GET").mockResolvedValue(answer(status, body) as never);
}

async function changePassword(current = CURRENT) {
  await userEvent.type(screen.getByLabelText("Current password"), current);
  await userEvent.type(screen.getByLabelText("New password"), NEXT);
  await userEvent.click(screen.getByRole("button", { name: "Change password" }));
}

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
  window.sessionStorage.clear();
});

describe("the account route", () => {
  it("sends a visitor with no token to sign in, without rendering the page first", async () => {
    const get = vi.spyOn(api, "GET");

    await renderAt("/account");

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Account" })).not.toBeInTheDocument();
    // The guard runs in `beforeLoad`, so nothing on the page ever asked the API for anything.
    expect(get).not.toHaveBeenCalled();
  });

  it("renders the user the server reports, not the one the token claims", async () => {
    setToken("header.payload.signature");
    meReturns(200, USER);

    await renderAt("/account");

    expect(await screen.findByText(EMAIL)).toBeInTheDocument();
    expect(screen.getByText(USER.id)).toBeInTheDocument();
    expect(screen.getByText("active")).toBeInTheDocument();
  });

  it("surfaces a failure to load the account instead of an empty panel", async () => {
    setToken("header.payload.signature");
    meReturns(503, { error: { code: "unavailable", message: "Database is down" } });

    await renderAt("/account");

    expect(await screen.findByRole("alert")).toHaveTextContent("Database is down");
  });
});

describe("changing the password", () => {
  it("clears the form once the backend has answered 204", async () => {
    setToken("header.payload.signature");
    meReturns(200, USER);
    const post = vi.spyOn(api, "POST").mockResolvedValue(answer(204) as never);
    await renderAt("/account");
    await screen.findByText(EMAIL);

    await changePassword();

    expect(post).toHaveBeenCalledWith("/api/v1/auth/change-password", {
      body: { current_password: CURRENT, new_password: NEXT },
    });
    expect(await screen.findByLabelText("Current password")).toHaveValue("");
  });

  it("shows a wrong current password on the field, and keeps the session", async () => {
    // The backend answers 403 rather than 401 exactly so this happens: the session is valid, and
    // only this operation was refused. A 401 would have signed the user out mid-form.
    setToken("header.payload.signature");
    meReturns(200, USER);
    vi.spyOn(api, "POST").mockResolvedValue(
      answer(403, {
        error: { code: "forbidden", message: "Current password is incorrect" },
      }) as never,
    );
    await renderAt("/account");
    await screen.findByText(EMAIL);

    await changePassword("not-the-right-one");

    expect(await screen.findByRole("alert")).toHaveTextContent("Current password is incorrect");
    expect(getToken()).toBe("header.payload.signature");
    expect(screen.getByRole("heading", { name: "Account" })).toBeInTheDocument();
  });
});
