import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "@/lib/api/client";
import { clearToken, getToken } from "@/lib/auth";
import { renderAt } from "@/test-router";

const EMAIL = "ada@example.com";
const PASSWORD = "correct-horse-battery";

const USER = {
  id: "0f4f2b7e-6a4a-4f2e-9a3f-1c2d3e4f5a6b",
  email: EMAIL,
  is_active: true,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

const TOKEN = { access_token: "header.payload.signature", token_type: "bearer", expires_in: 900 };

function answer(status: number, body: unknown) {
  return {
    data: status < 400 ? body : undefined,
    error: status < 400 ? undefined : body,
    response: new Response(null, { status }),
  };
}

async function fillIn(password = PASSWORD) {
  await userEvent.type(screen.getByLabelText("Email"), EMAIL);
  await userEvent.type(screen.getByLabelText("Password"), password);
  await userEvent.click(screen.getByRole("button", { name: "Create account" }));
}

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
  window.sessionStorage.clear();
});

describe("registering", () => {
  it("creates the account and signs in with it, without asking for the password twice", async () => {
    vi.spyOn(api, "POST")
      .mockResolvedValueOnce(answer(201, USER) as never)
      .mockResolvedValueOnce(answer(200, TOKEN) as never);
    vi.spyOn(api, "GET").mockResolvedValue(answer(200, USER) as never);
    await renderAt("/register");

    await fillIn();

    expect(await screen.findByRole("heading", { name: "Account" })).toBeInTheDocument();
    expect(getToken()).toBe(TOKEN.access_token);
  });

  it("shows that the address is taken — which registration, unlike sign-in, does disclose", async () => {
    vi.spyOn(api, "POST").mockResolvedValue(
      answer(409, {
        error: { code: "conflict", message: "An account with that email already exists" },
      }) as never,
    );
    await renderAt("/register");

    await fillIn();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "An account with that email already exists",
    );
    expect(getToken()).toBeNull();
  });

  it("refuses a short password before spending a round trip on it", async () => {
    const post = vi.spyOn(api, "POST");
    await renderAt("/register");

    await fillIn("short");

    expect(await screen.findByRole("alert")).toHaveTextContent("At least 12 characters");
    expect(post).not.toHaveBeenCalled();
  });
});
