import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ErrorBoundary } from "./error-boundary";

function Boom({ explode }: { explode: boolean }) {
  if (explode) throw new Error("kaboom");
  return <p>rendered fine</p>;
}

/** Lets a test flip the child from throwing to working, which is what `reset` is for. */
function Harness() {
  const [explode, setExplode] = useState(true);

  function fallback(error: Error, reset: () => void) {
    function retry() {
      setExplode(false);
      reset();
    }
    return (
      <div>
        <p>caught: {error.message}</p>
        <button onClick={retry}>retry</button>
      </div>
    );
  }

  return (
    <ErrorBoundary fallback={fallback}>
      <Boom explode={explode} />
    </ErrorBoundary>
  );
}

function silentFallback() {
  return <p>fallback</p>;
}

// React logs every caught render error to the console. Expected here, and noise in the output.
beforeEach(() => vi.spyOn(console, "error").mockImplementation(() => undefined));
afterEach(() => vi.restoreAllMocks());

describe("ErrorBoundary", () => {
  it("renders the fallback with the error instead of unmounting the tree", () => {
    render(<Harness />);
    expect(screen.getByText("caught: kaboom")).toBeInTheDocument();
  });

  it("re-mounts the subtree after reset, once the cause is gone", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "retry" }));
    expect(screen.getByText("rendered fine")).toBeInTheDocument();
  });

  it("renders children untouched when nothing throws", () => {
    render(
      <ErrorBoundary fallback={silentFallback}>
        <Boom explode={false} />
      </ErrorBoundary>,
    );
    expect(screen.getByText("rendered fine")).toBeInTheDocument();
    expect(screen.queryByText("fallback")).not.toBeInTheDocument();
  });
});
