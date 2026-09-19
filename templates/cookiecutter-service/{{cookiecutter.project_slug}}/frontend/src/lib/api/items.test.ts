import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "./client";
import { api } from "./client";
import { createItem, deleteItem, itemKeys, listItems } from "./items";

afterEach(() => vi.restoreAllMocks());

function ok(status: number, data?: unknown) {
  return { data, error: undefined, response: new Response(null, { status }) };
}

describe("listItems", () => {
  it("sends the pagination the backend validates, and returns the body", async () => {
    const get = vi.spyOn(api, "GET").mockResolvedValue(ok(200, { items: [], count: 0 }) as never);

    await expect(listItems(25, 50)).resolves.toEqual({ items: [], count: 0 });
    expect(get).toHaveBeenCalledWith("/api/v1/items", {
      params: { query: { limit: 25, offset: 50 } },
    });
  });
});

describe("createItem", () => {
  it("surfaces the backend's error code so the form can react to it", async () => {
    vi.spyOn(api, "POST").mockResolvedValue({
      data: undefined,
      error: { error: { code: "item_name_taken", message: "Already exists" } },
      response: new Response(null, { status: 409 }),
    } as never);

    await expect(createItem({ name: "widget" })).rejects.toMatchObject({
      code: "item_name_taken",
      status: 409,
    });
  });
});

describe("deleteItem", () => {
  it("treats the backend's 204 as success rather than a missing body", async () => {
    vi.spyOn(api, "DELETE").mockResolvedValue(ok(204) as never);
    await expect(deleteItem("abc")).resolves.toBeUndefined();
  });

  it("throws when the item is gone", async () => {
    vi.spyOn(api, "DELETE").mockResolvedValue({
      data: undefined,
      error: { error: { code: "item_not_found", message: "No such item" } },
      response: new Response(null, { status: 404 }),
    } as never);
    await expect(deleteItem("abc")).rejects.toBeInstanceOf(ApiError);
  });
});

describe("itemKeys", () => {
  it("nests every key under the collection, so one invalidation reaches them all", () => {
    expect(itemKeys.list(50, 0)[0]).toBe(itemKeys.all[0]);
    expect(itemKeys.detail("abc")[0]).toBe(itemKeys.all[0]);
  });
});
