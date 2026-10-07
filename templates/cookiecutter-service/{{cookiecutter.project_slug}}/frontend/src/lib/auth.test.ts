import { afterEach, describe, expect, it, vi } from "vitest";

import {
  clearToken,
  endSession,
  enforceSession,
  getToken,
  refreshOnce,
  setToken,
  subscribe,
} from "./auth";

function answer(status: number): Response {
  return new Response(null, { status });
}

function storedValues(storage: Storage): (string | null)[] {
  return Array.from({ length: storage.length }, (_, i) => storage.getItem(storage.key(i) ?? ""));
}

/** A refresh the test finishes by hand, so it can line callers up while it is in flight. */
function pendingRefresh() {
  let finish: (token: string | null) => void = () => undefined;
  const refresh = vi.fn(
    () =>
      new Promise<string | null>((resolve) => {
        finish = resolve;
      }),
  );
  return { refresh, finish: (token: string | null) => finish(token) };
}

afterEach(() => {
  clearToken();
  window.history.replaceState(null, "", "/");
});

describe("the token store", () => {
  it("hands back what was stored", () => {
    setToken("header.payload.signature");

    expect(getToken()).toBe("header.payload.signature");
  });

  it("keeps the access token out of web storage", () => {
    // Memory only. The refresh cookie rebuilds the session after a reload, so there is nothing
    // to gain from a copy any script in the page could read for as long as it lives. (The module
    // touches no store at all; `localStorage` is not read here because some Node versions shadow
    // jsdom's with one that warns on every access.)
    setToken("header.payload.signature");

    expect(storedValues(window.sessionStorage)).not.toContain("header.payload.signature");
  });

  it("forgets the token", () => {
    setToken("a.b.c");

    clearToken();

    expect(getToken()).toBeNull();
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

describe("refreshOnce", () => {
  it("stores the token the refresh returns", async () => {
    await expect(refreshOnce(async () => "fresh.access.token")).resolves.toBe("fresh.access.token");

    expect(getToken()).toBe("fresh.access.token");
  });

  it("shares one refresh between every caller that asks while it is in flight", async () => {
    // A refresh token is single-use. Two refreshes with the same cookie look like a replay to
    // the backend, and it answers a replay by ending the whole session.
    const { refresh, finish } = pendingRefresh();

    const callers = [refreshOnce(refresh), refreshOnce(refresh), refreshOnce(refresh)];
    // The refresh may start a tick later, once the cross-tab lock is granted.
    await vi.waitFor(() => expect(refresh).toHaveBeenCalled());
    finish("fresh.access.token");

    await expect(Promise.all(callers)).resolves.toEqual([
      "fresh.access.token",
      "fresh.access.token",
      "fresh.access.token",
    ]);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("starts a new refresh once the last one has finished", async () => {
    const refresh = vi.fn(async () => "fresh.access.token");

    await refreshOnce(refresh);
    await refreshOnce(refresh);

    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("answers null when the backend refuses, and leaves the token alone", async () => {
    // Deciding that the session is over is the caller's job, not this function's.
    setToken("stale.access.token");

    await expect(refreshOnce(async () => null)).resolves.toBeNull();

    expect(getToken()).toBe("stale.access.token");
  });

  it("treats a network failure as a refusal rather than an unhandled rejection", async () => {
    await expect(
      refreshOnce(async () => {
        throw new TypeError("Failed to fetch");
      }),
    ).resolves.toBeNull();
  });
});

describe("endSession", () => {
  it("forgets the token and sends the caller to sign in", () => {
    setToken("a.b.c");
    const redirect = vi.fn();

    endSession(redirect);

    expect(getToken()).toBeNull();
    expect(redirect).toHaveBeenCalledWith("/login");
  });

  it("does not redirect to the page it is already on", () => {
    // Without this guard, a 401 answered to something on /login reloads /login, which requests
    // again, which 401s again. That is the loop.
    window.history.replaceState(null, "", "/login");
    setToken("a.b.c");
    const redirect = vi.fn();

    endSession(redirect);

    expect(getToken()).toBeNull();
    expect(redirect).not.toHaveBeenCalled();
  });
});

describe("enforceSession", () => {
  it("ends the session on a 401 while signed in", () => {
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
});
