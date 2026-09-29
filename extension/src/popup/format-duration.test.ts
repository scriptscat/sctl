import { describe, expect, it } from "vitest";
import { formatConnectedSince } from "./format-duration";
import { stringsFor } from "@/i18n";

const s = stringsFor("en");

describe("formatConnectedSince", () => {
  it("says 'just now' for less than a minute", () => {
    expect(formatConnectedSince(s, 1000, 1000 + 30_000)).toBe("just now");
  });

  it("formats whole minutes under an hour", () => {
    expect(formatConnectedSince(s, 0, 5 * 60_000)).toBe("5 min");
  });

  it("formats whole hours at and beyond 60 minutes", () => {
    expect(formatConnectedSince(s, 0, 90 * 60_000)).toBe("1 h");
  });
});
