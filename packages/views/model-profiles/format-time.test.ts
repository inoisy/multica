// @vitest-environment node
import { describe, expect, it } from "vitest";
import { formatModelTime } from "./format-time";

describe("forecast display", () => {
  it("formats the same instant consistently for profile and pool precision", () => {
    for (const locale of ["en-US", "ru-RU"]) {
      const profile = formatModelTime("2026-10-25T12:30:59.123456789Z", locale);
      expect(profile).toBe(formatModelTime("2026-10-25T12:30:59Z", locale));
      expect(profile).not.toContain("123456789");
    }
  });
  it("does not expose invalid or missing timestamps", () => {
    expect(formatModelTime(null)).toBe("");
    expect(formatModelTime("invalid")).toBe("");
  });
});
