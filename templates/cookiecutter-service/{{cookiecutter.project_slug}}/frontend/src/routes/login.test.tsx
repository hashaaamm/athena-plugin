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

function loginReturns(status: number, body: unknown) {
  return vi.spyOn(api, "POST").mockResolvedValue({
    data: status < 400 ? body : undefined,
    error: status < 400 ? undefined : body,
    response: new Response(null, { status }),
  } as never);
}

async function signIn(password = PASSWORD) {
  await userEvent.type(screen.getByLabelText("Email"), EMAIL);
  await userEvent.type(screen.getByLabelText("Password"), password);
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

afterEach(() => {
  vi.restoreAllMocks();
  clearToken();
  window.sessionStorage.clear();
});

describe("signing in", () => {
  it("stores the token and lands on the account page", async () => {
    loginReturns(200, TOKEN);
    vi.spyOn(api, "GET").mockResolvedValue({
      data: USER,
      error: undefined,
      response: new Response(null, { status: 200 }),
    } as never);
    await renderAt("/login");

    await signIn();

    expect(await screen.findByRole("heading", { name: "Account" })).toBeInTheDocument();
    expect(getToken()).toBe(TOKEN.access_token);
  });

  it("never says whether the address is registered", async () => {
    // The backend answers a wrong password and an unknown email identically, on purpose. This
    // test exists because the easiest thing in the world is to "improve" that on the client.
    loginReturns(401, { error: { code: "unauthorized", message: "Invalid email or password" } });
    await renderAt("/login");

    await signIn("wrong-password-here");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Invalid email or password.");
    expect(alert.textContent).not.toMatch(/no (such )?account|not found|does not exist|unknown/i);
    expect(getToken()).toBeNull();
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });

  it("answers a too-short password the same way, rather than confirming a length rule", async () => {
    // A 422 on sign-in means "no password of ours is that short". Told apart from a 401 on
    // screen, it is an oracle; here it is the same sentence.
    loginReturns(422, { detail: [{ msg: "String should have at least 12 characters" }] });
    await renderAt("/login");

    await signIn("short");

    expect(await screen.findByRole("alert")).toHaveTextContent("Invalid email or password.");
  });

  it("does not call the API to discover that the form is empty", async () => {
    const post = vi.spyOn(api, "POST");
    await renderAt("/login");

    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("Enter your email address")).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
  });
});
