import { describe, expect, it } from "vitest";

import { formatBuildVersion, formatRelativeDay } from "./format";

// A fixed "now" — the whole reason formatRelativeDay takes one. Local time, deliberately: the
// function compares calendar days in the reader's timezone, so a "Z" here would make the suite
// pass in UTC and fail an hour either side of midnight for everyone else.
const NOW = new Date("2024-06-24T12:00:00");

describe("formatBuildVersion", () => {
  it("abbreviates a commit SHA and leaves a short identifier alone", () => {
    expect(formatBuildVersion("444e2e3be7532f379b84f7d892ce0a7c8995298")).toBe("444e2e3");
    expect(formatBuildVersion("dev")).toBe("dev");
    expect(formatBuildVersion(undefined)).toBe("—");
  });
});

describe("formatRelativeDay", () => {
  it("labels today and yesterday by name", () => {
    expect(formatRelativeDay("2024-06-24T09:00:00", NOW)).toBe("Today");
    expect(formatRelativeDay("2024-06-23T23:00:00", NOW)).toBe("Yesterday");
  });

  it("counts days inside the last week", () => {
    expect(formatRelativeDay("2024-06-21T09:00:00", NOW)).toBe("3 days ago");
  });

  it("collapses the second week and dates anything older", () => {
    expect(formatRelativeDay("2024-06-16T09:00:00", NOW)).toBe("Last week");
    expect(formatRelativeDay("2024-05-24T09:00:00", NOW)).toBe("24 May");
  });

  it("treats a future timestamp as today rather than a negative count", () => {
    expect(formatRelativeDay("2024-06-30T09:00:00", NOW)).toBe("Today");
  });
});
