"use client";

import { useEffect, useState } from "react";
import type { AvailabilityVerdict } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useAvailabilityText } from "../../model-profiles";
import { verdictVariant } from "./verdict-badge";

/** How far ahead a lane looks, in hours. */
export const LANE_HORIZONS = [12, 48, 168] as const;
export type LaneHorizon = (typeof LANE_HORIZONS)[number];

const BUSY_TONE = {
  warning: "bg-warning/55",
  destructive: "bg-destructive/50",
  outline: "bg-muted-foreground/30",
  success: "bg-success/45",
} as const;

function useMinuteClock(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(id);
  }, []);
  return now;
}

/**
 * One account's (or pool's) availability on a time scale: busy until the
 * server's time, free after. A time that is a retry estimate is hatched and an
 * unknown end fills the lane, so an estimate never looks like a reported reset.
 */
export function AvailabilityLane({ verdict, horizon, className }: { verdict: AvailabilityVerdict; horizon: LaneHorizon; className?: string }) {
  const now = useMinuteClock();
  const availability = useAvailabilityText();
  const free = verdict.usable && !verdict.blocked;
  const end = verdict.until ? Date.parse(verdict.until) : NaN;
  const share = Number.isNaN(end) ? 1 : Math.min(1, Math.max(0, (end - now) / (horizon * 3_600_000)));
  const hatched = verdict.until_estimated || Number.isNaN(end);
  const label = free ? availability.describe(verdict) : `${availability.describe(verdict)} · ${availability.availableAt(verdict)}`;
  return (
    <div role="img" aria-label={label} title={label} className={cn("relative h-4 w-full", className)} data-testid="availability-lane">
      <span aria-hidden="true" className="absolute left-0 top-0 h-full w-0.5 rounded-sm bg-foreground/60" />
      {free ? (
        <span aria-hidden="true" className={cn("absolute inset-x-0 top-1/2 h-1 -translate-y-1/2 rounded-sm", BUSY_TONE.success)} />
      ) : (
        <>
          <span
            aria-hidden="true"
            className={cn("absolute left-0 top-0 h-full rounded-l-md", BUSY_TONE[verdictVariant(verdict)], hatched && "opacity-60 [background-image:repeating-linear-gradient(135deg,transparent_0_3px,var(--background)_3px_5px)]")}
            style={{ width: `${share * 100}%` }}
          />
          {share < 1 ? (
            <span aria-hidden="true" className={cn("absolute top-1/2 h-1 -translate-y-1/2 rounded-sm", BUSY_TONE.success)} style={{ left: `${share * 100}%`, right: 0 }} />
          ) : null}
        </>
      )}
    </div>
  );
}
