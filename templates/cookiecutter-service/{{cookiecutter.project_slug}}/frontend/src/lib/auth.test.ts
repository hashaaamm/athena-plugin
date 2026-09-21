import { afterEach, describe, expect, it, vi } from "vitest";

import { clearToken, enforceSession, getToken, setToken, subscribe } from "./auth";

const STORAGE_KEY = "{{ cookiecutter.project_slug }}.access_token";

function answer(status: number): Response {
  return new Response(null, { status });
}

afterEach(() => {
  clearToken();
  window.sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

describe("the token store", () => {
  it("hands back what was stored, and persists it across a reload", () => {
    setToken("header.payload.signature");

    expect(getToken()).toBe("header.payload.signature");
    // The durable copy: this is what survives F5, and the reason it is not in-memory only.
    expect(window.sessionStorage.getItem(STORAGE_KEY)).toBe("header.payload.signature");
  });

  it("forgets the token from memory and from storage together", () => {
    setToken("a.b.c");

    clearToken();

    expect(getToken()).toBeNull();
    expect(window.sessionStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("tells subscribers about sign-in and sign-out, and stops when they leave", () => {
    const listener = vi.fn();
    const unsubscribe = subscribe(listener);

    setToken("a.b.c");
    clearToken();
    unsubscribe();
    setToken("d.e.f");

    expect(listener).toHaveBeenCalledTimes(2);
  });
});

describe("enforceSession", () => {
  it("ends the session and sends the caller to sign in", () => {
    setToken("a.b.c");
    const redirect = vi.fn();

    enforceSession(answer(401), redirect);

    expect(getToken()).toBeNull();
    expect(redirect).toHaveBeenCalledWith("/login");
  });

  it("ignores a 401 when we were not signed in — that is a failed login, not a lost session", () => {
    const redirect = vi.fn();

    enforceSession(answer(401), redirect);

    expect(redirect).not.toHaveBeenCalled();
  });

  it("leaves a 403 alone: the session is valid and only the operation was refused", () => {
    // This is the whole reason `POST /auth/change-password` answers 403 for a wrong current
    // password. A 401 there would log the user out mid-form.
    setToken("a.b.c");
    const redirect = vi.fn();

    enforceSession(answer(403), redirect);

    expect(getToken()).toBe("a.b.c");
    expect(redirect).not.toHaveBeenCalled();
  });

  it("does nothing at all on a successful response", () => {
    setToken("a.b.c");
    const redirect = vi.fn();

    enforceSession(answer(200), redirect);

    expect(getToken()).toBe("a.b.c");
    expect(redirect).not.toHaveBeenCalled();
  });

  it("does not redirect to the page it is already on", () => {
    // Without this guard, a 401 answered to something on /login reloads /login, which requests
    // again, which 401s again. That is the loop.
    window.history.replaceState(null, "", "/login");
    setToken("a.b.c");
    const redirect = vi.fn();

    enforceSession(answer(401), redirect);

    expect(getToken()).toBeNull();
    expect(redirect).not.toHaveBeenCalled();
  });
});
