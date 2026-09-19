import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { components } from "./schema";
import { api, assertOk, unwrap } from "./client";

export type Item = components["schemas"]["ItemRead"];
export type ItemCreate = components["schemas"]["ItemCreate"];

/**
 * Query keys in one object, not sprinkled as string literals. A typo in an invalidation key is
 * silent: the mutation succeeds, the list never refreshes, and it looks like a caching bug.
 */
export const itemKeys = {
  all: ["items"] as const,
  list: (limit: number, offset: number) => ["items", "list", limit, offset] as const,
  detail: (id: string) => ["items", "detail", id] as const,
};

// --- Fetchers ------------------------------------------------------------
//
// Plain async functions, with the hooks below as thin wrappers. Testing a fetcher needs no
// React, no provider and no renderHook — which is why these are the functions with tests.

export async function listItems(limit: number, offset: number) {
  return unwrap(
    await api.GET("/api/v1/items", {
      params: { query: { limit, offset } },
    }),
  );
}

export async function createItem(body: ItemCreate) {
  return unwrap(await api.POST("/api/v1/items", { body }));
}

export async function deleteItem(id: string) {
  assertOk(
    await api.DELETE("/api/v1/items/{item_id}", {
      params: { path: { item_id: id } },
    }),
  );
}

// --- Hooks ---------------------------------------------------------------

export function useItems(limit = 50, offset = 0) {
  return useQuery({
    queryKey: itemKeys.list(limit, offset),
    queryFn: () => listItems(limit, offset),
  });
}

export function useCreateItem() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createItem,
    // Invalidate the collection, never patch it by hand: the server decides ids, timestamps and
    // ordering, and a locally assembled row is a guess that drifts.
    onSuccess: () => queryClient.invalidateQueries({ queryKey: itemKeys.all }),
  });
}

export function useDeleteItem() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: deleteItem,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: itemKeys.all }),
  });
}
