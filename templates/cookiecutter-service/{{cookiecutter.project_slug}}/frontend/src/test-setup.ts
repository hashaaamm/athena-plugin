import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// jest-dom's matchers (toBeInTheDocument, toHaveTextContent, …), added to Vitest's expect.
import "@testing-library/jest-dom/vitest";

// Testing Library unmounts between tests automatically ONLY when Vitest's globals are enabled.
// They are not — every test imports what it uses — so cleanup is wired up here instead. Without
// it the previous test's DOM is still mounted and the next query finds two of everything, which
// reads as a duplicate-render bug in the component rather than a missing line in this file.
afterEach(cleanup);
