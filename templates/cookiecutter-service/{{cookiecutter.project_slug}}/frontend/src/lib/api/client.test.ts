import { describe, expect, it } from "vitest";

import { ApiError, assertOk, unwrap } from "./client";

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
