import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import type { SubscriptionUsage } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { sub } from "./accounts.fixtures";
import { UsageMeters } from "./usage-meters";

const snapshot = (observedAt: Date, used: number): SubscriptionUsage => ({
  window: "week", used_percent: used, resets_at: null, limit_reached: false, source: "daemon", observed_at: observedAt.toISOString(),
});

function meters(observedAt: Date) {
  const s = sub("zai", { caps: { week: 80 }, usage: [snapshot(observedAt, 80)] });
  return renderWithI18n(<UsageMeters account={{ id: "zai", subscriptions: [s], usage: s.usage }} windowName={(w) => w} compact />);
}

describe("UsageMeters", () => {
  it("shows a fresh snapshot as the current load", () => {
    meters(new Date());
    expect(screen.queryByTestId("usage-measured-at")).toBeNull();
    expect(screen.getByRole("list").dataset.stale).toBeUndefined();
  });

  it("dates and dims a snapshot the server no longer counts", () => {
    meters(new Date(Date.now() - 10 * 24 * 60 * 60 * 1000));
    expect(screen.getByTestId("usage-measured-at")).toBeTruthy();
    expect(screen.getByRole("list").dataset.stale).toBe("true");
  });
});
