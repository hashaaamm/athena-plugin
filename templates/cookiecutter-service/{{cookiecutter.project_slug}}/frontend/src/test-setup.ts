import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// jest-dom's matchers (toBeInTheDocument, toHaveTextContent, …), added to Vitest's expect.
import "@testing-library/jest-dom/vitest";

// Testing Library unmounts between tests automatically ONLY when Vitest's globals are enabled.
// They are not — every test imports what it uses — so cleanup is wired up here instead. Without
// it the previous test's DOM is still mounted and the next query finds two of everything, which
// reads as a duplicate-render bug in the component rather than a missing line in this file.
afterEach(cleanup);

// jsdom implements no layout, so it refuses to scroll and prints "Not implemented: Window's
// scrollTo()" to stderr for every test that navigates — TanStack Router resets the scroll
// position on each navigation. A no-op keeps a passing suite's output readable, which is the
// only way anybody notices the one line in it that matters.
Object.defineProperty(window, "scrollTo", { value: () => undefined, writable: true });
