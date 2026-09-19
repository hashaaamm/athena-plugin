import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "@/lib/api/client";
import { ItemsPage } from "./items";

afterEach(() => vi.restoreAllMocks());

const ITEM = {
  id: "0f4f2b7e-6a4a-4f2e-9a3f-1c2d3e4f5a6b",
  name: "widget",
  description: "the example resource",
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
};

function listReturns(body: unknown, status = 200) {
  return vi.spyOn(api, "GET").mockResolvedValue({
    data: status === 200 ? body : undefined,
    error: status === 200 ? undefined : body,
    response: new Response(null, { status }),
  } as never);
}

/**
 * Renders the page against a throwaway QueryClient. Retries are off: with them on, a test for
 * the error state waits out three backoffs before the assertion can pass.
 */
function renderPage() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
    },
  });

  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  }

  return render(<ItemsPage />, { wrapper: Wrapper });
}

describe("ItemsPage", () => {
  it("lists what the API returns", async () => {
    listReturns({ items: [ITEM], count: 1 });
    renderPage();

    expect(await screen.findByText("widget")).toBeInTheDocument();
    expect(screen.getByText("the example resource")).toBeInTheDocument();
  });

  it("shows the empty state rather than an empty table", async () => {
    listReturns({ items: [], count: 0 });
    renderPage();

    expect(await screen.findByText("No items yet")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("surfaces the backend's message when the list fails", async () => {
    listReturns({ error: { code: "database_unavailable", message: "Database is down" } }, 503);
    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent("Database is down");
  });

  it("refuses to submit an empty name, and does not call the API to find out", async () => {
    listReturns({ items: [], count: 0 });
    const post = vi.spyOn(api, "POST");
    renderPage();

    await userEvent.click(await screen.findByRole("button", { name: "Add item" }));

    expect(await screen.findByText("A name is required")).toBeInTheDocument();
    expect(post).not.toHaveBeenCalled();
  });
});
